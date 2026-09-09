package provision

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/onboard"
)

// Service owns provision jobs: single-flight enqueue, the install→freshness→
// generate pipeline, durable status, and startup self-heal. Async execution is
// the caller's job (api.spawnProvision), mirroring internal/improve.
type Service struct {
	DB      *sql.DB
	Runner  Runner
	Actions map[string]GenerateAction
	// ClaudeDir anchors the user-level settings.json whose enabledPlugins key the
	// symlinked-overlay fallback has to revert. Defaults to ~/.claude.
	ClaudeDir string
	sem       chan struct{}    // global concurrency cap
	now       func() time.Time // test seam
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
		DB: db, Runner: r, Actions: defaultActions(), ClaudeDir: defaultClaudeDir(),
		sem: make(chan struct{}, maxConcurrent), now: time.Now,
	}
}

func defaultClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
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
	return s.run(ctx, jobID, projectPath, pack, false)
}

// RunForce is Run with the freshness guard bypassed — an explicit user-requested
// rebuild regenerates the artifact even when HEAD == analyzedAtCommit.
func (s *Service) RunForce(ctx context.Context, jobID int64, projectPath, pack string) error {
	return s.run(ctx, jobID, projectPath, pack, true)
}

func (s *Service) run(ctx context.Context, jobID int64, projectPath, pack string, force bool) error {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	// Every CLI call in this pipeline carries projectPath as its dir, even the
	// ones whose outcome does not depend on cwd: the dir is what binds the call
	// to the project's Claude account (Runner resolves CLAUDE_CONFIG_DIR from
	// it). A bare "" runs against the default ~/.claude, so a project bound to
	// another account would have its marketplace index refreshed and its pack
	// installed in a config dir the generate step never reads.
	s.set(jobID, "installing", "updating marketplace index", "", false)
	if _, err := s.Runner.Claude(ctx, projectPath, "", "plugin", "marketplace", "update", "swarmery"); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}
	s.set(jobID, "installing", "installing "+pack, "", false)
	if err := s.install(ctx, projectPath, pack); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}

	act, has := s.Actions[pack]
	if !has {
		s.set(jobID, "installed", "installed", "", true)
		return nil
	}
	// Cheap refresh first, gate second: the gate may decide the map's commit is
	// current and skip the LLM, and the viewer must not be left behind by that.
	if act.Refresh != nil {
		s.set(jobID, "generating", "re-rendering viewer from the existing map", "", false)
		if err := act.Refresh(ctx, s, projectPath); err != nil {
			log.Printf("provision: %s refresh for %s: %v", pack, projectPath, err)
		}
	}
	if !force && act.Fresh != nil && act.Fresh(projectPath) {
		s.set(jobID, "skipped", "artifact already current", "", true)
		return nil
	}

	s.set(jobID, "generating", "running "+pack+" generator", "", false)
	gctx, cancel := context.WithTimeout(ctx, act.Timeout)
	defer cancel()
	// A generate action's whole product is files (architecture-out/** for the
	// architecture pack), and a headless run without --permission-mode can write
	// nothing — not even inside its own cwd — while still exiting 0. Without the
	// flag the job reports "generated" over an artifact that does not exist, and
	// Fresh() then reruns it on every toggle. See internal/claudeflags.
	genArgs := append([]string{"-p", "--model", defaultModel, "--output-format", "text"},
		claudeflags.PermissionModeArgs(permEnv)...)
	if _, err := s.Runner.Claude(gctx, projectPath, act.Prompt, genArgs...); err != nil {
		s.set(jobID, "failed", "", err.Error(), true)
		return err
	}
	s.set(jobID, "done", "generated", "", true)
	return nil
}

// install puts the pack where the project that asked for it can load it, and
// only there.
//
// The scope is the whole point: `claude plugin install` defaults to --scope
// user, which installs AND enables the pack for every project on the machine —
// the opposite of what a per-project dashboard toggle means. Passing the project
// path as cwd is what makes --scope project resolve to this project at all.
//
// A symlinked <project>/.claude is the one case project scope cannot serve: the
// CLI refuses to write settings through it (SymlinkWriteRefusedError), so that
// consumer falls back to a user-scope install with the global enable reverted
// afterwards — the same trade-off, and the same guard, as the repair endpoint's
// (see internal/onboard/globalenable.go for why the revert keeps the install
// drift-resolving). The revert runs even when the CLI failed: a partial install
// can still have written the key.
func (s *Service) install(ctx context.Context, projectPath, pack string) error {
	id := pack + "@swarmery"
	if !onboard.SymlinkedClaudeDir(projectPath) {
		_, err := s.Runner.Claude(ctx, projectPath, "", "plugin", "install", id, "--scope", "project")
		return err
	}

	// The user settings.json to snapshot is the one the install will write: the
	// account's config dir when the project is bound to one, else the default.
	claudeDir := s.ClaudeDir
	if d, ok := claudeacct.ConfigDirForProject(projectPath); ok {
		claudeDir = d
	}
	snap, cerr := onboard.CaptureGlobalEnable(claudeDir, id)
	if cerr != nil {
		// Refuse rather than risk an irreversible global enable.
		return fmt.Errorf("refusing to install %s: %s has a symlinked .claude, so only a "+
			"user-scope install is possible, and that would enable the pack for every "+
			"project on this machine without a way to revert it: %w", id, projectPath, cerr)
	}
	// projectPath as dir binds the account env; user scope itself ignores cwd.
	_, ierr := s.Runner.Claude(ctx, projectPath, "", "plugin", "install", id)
	if rerr := snap.Restore(); rerr != nil {
		log.Printf("provision: user-scope install of %s left the global enable in place: %v", id, rerr)
	}
	return ierr
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
