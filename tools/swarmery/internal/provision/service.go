package provision

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Service owns provision jobs: single-flight enqueue, the install→freshness→
// generate pipeline, durable status, and startup self-heal. Async execution is
// the caller's job (api.spawnProvision), mirroring internal/improve.
type Service struct {
	DB      *sql.DB
	Runner  Runner
	Actions map[string]GenerateAction
	sem     chan struct{}    // global concurrency cap
	now     func() time.Time // test seam
}

// Job is the DTO-facing snapshot of a provision_jobs row.
type Job struct {
	ID       int64
	Status   string
	LastLine string
	Error    string
}

const maxConcurrent = 2

func NewService(db *sql.DB, r Runner) *Service {
	return &Service{
		DB: db, Runner: r, Actions: defaultActions(),
		sem: make(chan struct{}, maxConcurrent), now: time.Now,
	}
}

func (s *Service) ts() string { return s.now().UTC().Format(time.RFC3339) }

// Enqueue creates a pending job for (projectID, pack) unless a non-terminal one
// already exists (single-flight). started=false means an existing job was
// returned (or the caller should not spawn a new goroutine).
func (s *Service) Enqueue(projectID int64, pack string) (id int64, started bool, err error) {
	var existing int64
	e := s.DB.QueryRow(
		`SELECT id FROM provision_jobs WHERE project_id=? AND pack=? AND status IN ('pending','installing','generating') LIMIT 1`,
		projectID, pack).Scan(&existing)
	if e == nil {
		return existing, false, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return 0, false, e
	}
	res, e := s.DB.Exec(
		`INSERT INTO provision_jobs(project_id, pack, status, started_at) VALUES(?,?,'pending',?)`,
		projectID, pack, s.ts())
	if e != nil {
		// A concurrent enable won the race between our SELECT and INSERT: the
		// partial unique index (idx_provision_jobs_inflight) rejects the second
		// in-flight row. Treat it as "already in flight" — re-read the winner.
		if again := s.DB.QueryRow(
			`SELECT id FROM provision_jobs WHERE project_id=? AND pack=? AND status IN ('pending','installing','generating') LIMIT 1`,
			projectID, pack).Scan(&existing); again == nil {
			return existing, false, nil
		}
		return 0, false, e
	}
	id, _ = res.LastInsertId()
	return id, true, nil
}

func (s *Service) set(id int64, status, lastLine, errMsg string, done bool) {
	if done {
		_, _ = s.DB.Exec(
			`UPDATE provision_jobs SET status=?, last_line=?, error=NULLIF(?, ''), finished_at=? WHERE id=?`,
			status, lastLine, errMsg, s.ts(), id)
		return
	}
	_, _ = s.DB.Exec(
		`UPDATE provision_jobs SET status=?, last_line=? WHERE id=?`, status, lastLine, id)
}

// Run executes the pipeline for jobID against projectPath. Blocks — the caller
// runs it in a goroutine. All failures are captured on the row (status='failed')
// and also returned for logging.
func (s *Service) Run(ctx context.Context, jobID int64, projectPath, pack string) error {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	s.set(jobID, "installing", "updating marketplace index", "", false)
	if _, err := s.Runner.Claude(ctx, "", "", "plugin", "marketplace", "update", "swarmery"); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}
	if _, err := s.Runner.Claude(ctx, "", "", "plugin", "install", pack+"@swarmery"); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}

	act, has := s.Actions[pack]
	if !has {
		s.set(jobID, "installed", "installed", "", true)
		return nil
	}
	if act.Fresh != nil && act.Fresh(projectPath) {
		s.set(jobID, "skipped", "artifact already current", "", true)
		return nil
	}

	s.set(jobID, "generating", "running "+pack+" generator", "", false)
	gctx, cancel := context.WithTimeout(ctx, act.Timeout)
	defer cancel()
	if _, err := s.Runner.Claude(gctx, projectPath, act.Prompt, "-p", "--output-format", "text"); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}
	s.set(jobID, "done", "generated", "", true)
	return nil
}

// HealStale marks non-terminal rows (from a crashed/restarted daemon) failed.
func (s *Service) HealStale() error {
	_, err := s.DB.Exec(
		`UPDATE provision_jobs SET status='failed', error='interrupted by daemon restart', finished_at=? WHERE status IN ('pending','installing','generating')`,
		s.ts())
	return err
}

// Latest returns the newest job for (projectID, pack) for the DTO.
func (s *Service) Latest(projectID int64, pack string) (Job, bool, error) {
	var j Job
	var lastLine, errStr sql.NullString
	err := s.DB.QueryRow(
		`SELECT id, status, COALESCE(last_line,''), COALESCE(error,'') FROM provision_jobs
		   WHERE project_id=? AND pack=? ORDER BY id DESC LIMIT 1`, projectID, pack).
		Scan(&j.ID, &j.Status, &lastLine, &errStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	j.LastLine, j.Error = lastLine.String, errStr.String
	return j, true, nil
}
