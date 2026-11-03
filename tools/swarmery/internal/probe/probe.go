// Package probe runs an agent change proposal before anyone adopts it
// (harness² phase 4, D9–D11): the agent's promptfoo suite
// (evals/agents/<agent>.yaml) runs once on the base agent and once on the
// patched copy, both in a throwaway temp tree, and each test case lands as one
// proposal_probes row (migration 0136).
//
// The deterministic result of a case is promptfoo's HARD assertions only;
// llm-rubric assertions are recorded in the row's reason but never decide
// pass/fail. Where base and variant agree, a blind judge (judge.go, prompt.go)
// picks between the two outputs without knowing which is which.
//
// ADVISORY BY CONTRACT. Nothing in this package writes agent_change_proposals:
// a probe never approves, applies or rejects. It refuses a proposal that is not
// proposed or approved, and it stops once SWARMERY_PROBE_MAX_USD is spent,
// recording the remaining cases as capped.
package probe

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	mrand "math/rand/v2"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SourcePromptfoo is proposal_probes.source for a promptfoo-suite probe;
// 'bench' is reserved for phase 6.
const SourcePromptfoo = "promptfoo"

// blind_choice vocabulary (migration 0136's CHECK).
const (
	ChoiceBase    = "base"
	ChoiceVariant = "variant"
	ChoiceTie     = "tie"
	ChoiceSkipped = "skipped"
	ChoiceCapped  = "capped"
	ChoiceNoSuite = "no_suite"
)

// Job states, in-memory only: the rows are the durable result, the job is how
// the dashboard knows a run is still going.
const (
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Typed failures the API maps to 404 / 422 / 409.
var (
	ErrProposalNotFound = errors.New("proposal not found")
	ErrNotProbeable     = errors.New("only proposed or approved proposals can be probed")
	ErrJobRunning       = errors.New("a probe is already running for this proposal")
)

// MaxUSDEnv caps one probe job's spend; DefaultMaxUSD applies when it is unset
// or not a positive number. A suite case on the opus tier costs cents to tens
// of cents per side, so the default covers a small suite and stops a large one.
const (
	MaxUSDEnv     = "SWARMERY_PROBE_MAX_USD"
	DefaultMaxUSD = 2.00
)

// MaxUSD resolves the cost cap from the environment.
func MaxUSD() float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(MaxUSDEnv)), 64); err == nil && v > 0 && !math.IsInf(v, 0) {
		return v
	}
	return DefaultMaxUSD
}

// Case is one test of the suite, already cut out of the suite file.
type Case struct {
	Index int    // position in the suite, 0-based
	ID    string // the case's description, or "case-N"
	YAML  []byte // a one-element YAML sequence holding the test
}

// CaseRun is one case on one side (base or variant).
type CaseRun struct {
	HardPass   bool    // every hard assertion passed on every provider row
	SoftPassed int     // llm-rubric (model-graded) assertions that passed — recorded, not decisive
	SoftTotal  int     // llm-rubric (model-graded) assertions seen
	Task       string  // the user task the agent was given (vars.task)
	Output     string  // the agent's visible answer
	USD        float64 // what promptfoo reported for the run
}

// SuiteRunner runs one case against one prepared tree (a directory laid out
// like the repo root, holding evals/ and the target's plugin dir). Production
// is PromptfooRunner; tests fake it.
type SuiteRunner interface {
	RunCase(ctx context.Context, tree string, c Case) (CaseRun, error)
}

// Proposal is the slice of agent_change_proposals a probe reads.
type Proposal struct {
	ID         int64
	Agent      string
	AgentPath  string
	TargetKind string
	TargetPath string
	BaseSHA256 string
	Diff       string
	Status     string
}

// target is the repo-relative file the proposal edits.
func (p Proposal) target() string {
	if p.TargetKind == "skill" {
		return p.TargetPath
	}
	return p.AgentPath
}

// Job is the in-memory state of one probe run.
type Job struct {
	ID         string  `json:"id"`
	ProposalID int64   `json:"proposal_id"`
	Status     string  `json:"status"`
	Error      string  `json:"error,omitempty"`
	USD        float64 `json:"usd"`
	MaxUSD     float64 `json:"max_usd"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at,omitempty"`
}

// Service runs probes. Construct with NewService in production; tests fill the
// fields directly.
type Service struct {
	DB     *sql.DB
	Source Source      // where the base tree comes from (GitSource in production)
	Suite  SuiteRunner // promptfoo
	Judge  Judge       // blind judge on ties
	// MaxUSD overrides the env cap when > 0 (tests).
	MaxUSD float64
	// Go replaces `go fn()` for the background job; nil in production.
	Go func(func())
	// Flip decides the A/B order per case: true puts the base output at A.
	// nil → a fair coin.
	Flip func() bool
	// Now stamps rows; nil → time.Now.
	Now func() time.Time

	mu   sync.Mutex
	jobs map[string]*Job
}

// NewService wires the production runners against the marketplace clone repo
// (the same checkout internal/improve generates from).
func NewService(db *sql.DB, repo string) *Service {
	return &Service{
		DB:     db,
		Source: GitSource{Repo: repo, Ref: "origin/main"},
		Suite:  PromptfooRunner{},
		Judge:  ClaudeJudge{},
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) maxUSD() float64 {
	if s.MaxUSD > 0 {
		return s.MaxUSD
	}
	return MaxUSD()
}

func (s *Service) flip() bool {
	if s.Flip != nil {
		return s.Flip()
	}
	return mrand.IntN(2) == 0
}

// LoadProposal reads the probe's view of one proposal.
func (s *Service) LoadProposal(id int64) (Proposal, error) {
	p := Proposal{ID: id}
	err := s.DB.QueryRow(`
		SELECT agent, agent_path, target_kind, target_path, base_sha256, diff, status
		  FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&p.Agent, &p.AgentPath, &p.TargetKind, &p.TargetPath, &p.BaseSHA256, &p.Diff, &p.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrProposalNotFound
	}
	return p, err
}

// probeable is the refusal rule: a probe is evidence for a decision still to
// be made, so only an undecided (proposed) or not-yet-applied (approved) row
// qualifies.
func probeable(status string) bool { return status == "proposed" || status == "approved" }

// Start validates synchronously and runs the probe in the background. It
// returns the new job's id; the rows land in proposal_probes as cases finish.
func (s *Service) Start(proposalID int64) (Job, error) {
	p, err := s.LoadProposal(proposalID)
	if err != nil {
		return Job{}, err
	}
	if !probeable(p.Status) {
		return Job{}, fmt.Errorf("%w (status %s)", ErrNotProbeable, p.Status)
	}
	job, err := s.register(proposalID)
	if err != nil {
		return job, err
	}
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: probe: job %s panic recovered: %v", job.ID, r)
				s.finish(job.ID, 0, fmt.Errorf("panic: %v", r))
			}
		}()
		usd, err := s.Run(context.Background(), job.ID, p)
		if err != nil {
			log.Printf("error: probe: job %s (proposal %d): %v", job.ID, proposalID, err)
		}
		s.finish(job.ID, usd, err)
	}
	if s.Go != nil {
		s.Go(run)
	} else {
		go run()
	}
	return job, nil
}

// register single-flights jobs per proposal.
func (s *Service) register(proposalID int64) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*Job{}
	}
	for _, j := range s.jobs {
		if j.ProposalID == proposalID && j.Status == JobRunning {
			return *j, ErrJobRunning
		}
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	j := &Job{
		ID:         fmt.Sprintf("probe-%d-%s", proposalID, hex.EncodeToString(b[:])),
		ProposalID: proposalID,
		Status:     JobRunning,
		MaxUSD:     s.maxUSD(),
		StartedAt:  s.now().UTC().Format(time.RFC3339),
	}
	s.jobs[j.ID] = j
	return *j, nil
}

func (s *Service) finish(id string, usd float64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.Status != JobRunning {
		return
	}
	j.USD = usd
	j.FinishedAt = s.now().UTC().Format(time.RFC3339)
	j.Status = JobDone
	if err != nil {
		j.Status = JobFailed
		j.Error = err.Error()
	}
}

// Jobs returns this daemon's jobs for one proposal, newest first.
func (s *Service) Jobs(proposalID int64) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Job{}
	for _, j := range s.jobs {
		if j.ProposalID == proposalID {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].StartedAt != out[k].StartedAt {
			return out[i].StartedAt > out[k].StartedAt
		}
		return out[i].ID > out[k].ID
	})
	return out
}

// Row is one proposal_probes row.
type Row struct {
	ID          int64   `json:"id"`
	ProposalID  int64   `json:"proposal_id"`
	JobID       string  `json:"job_id"`
	Source      string  `json:"source"`
	TaskID      string  `json:"task_id"`
	BasePass    *bool   `json:"base_pass"`
	VariantPass *bool   `json:"variant_pass"`
	BlindChoice string  `json:"blind_choice"`
	Reason      string  `json:"reason"`
	USD         float64 `json:"usd"`
	CreatedAt   string  `json:"created_at"`
}

// Rows lists a proposal's probe rows, newest job first, cases in run order.
func (s *Service) Rows(proposalID int64) ([]Row, error) {
	return ListRows(s.DB, proposalID)
}

// ListRows is Rows over any handle (the API reads through its read pool).
func ListRows(db *sql.DB, proposalID int64) ([]Row, error) {
	rows, err := db.Query(`
		SELECT id, proposal_id, job_id, source, task_id, base_pass, variant_pass,
		       blind_choice, reason, usd, created_at
		  FROM proposal_probes WHERE proposal_id = ?
		 ORDER BY (SELECT MIN(p2.id) FROM proposal_probes p2
		            WHERE p2.proposal_id = proposal_probes.proposal_id
		              AND p2.job_id = proposal_probes.job_id) DESC, id ASC`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		var bp, vp sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ProposalID, &r.JobID, &r.Source, &r.TaskID, &bp, &vp,
			&r.BlindChoice, &r.Reason, &r.USD, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.BasePass = nullBool(bp)
		r.VariantPass = nullBool(vp)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullBool(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	b := n.Int64 != 0
	return &b
}

func boolArg(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func (s *Service) insert(jobID string, proposalID int64, r Row) error {
	_, err := s.DB.Exec(`
		INSERT INTO proposal_probes
		  (proposal_id, job_id, source, task_id, base_pass, variant_pass, blind_choice, reason, usd, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		proposalID, jobID, SourcePromptfoo, r.TaskID, boolArg(r.BasePass), boolArg(r.VariantPass),
		r.BlindChoice, r.Reason, r.USD, s.now().UTC().Format(time.RFC3339))
	return err
}

// Run probes one proposal to completion and returns what it spent. It writes
// proposal_probes rows and nothing else. Exported for the API's synchronous
// tests and for a CLI caller; Start is the background path.
func (s *Service) Run(ctx context.Context, jobID string, p Proposal) (float64, error) {
	target := p.target()
	if p.TargetKind == "skill" || !isAgentFile(target) {
		return 0, s.insert(jobID, p.ID, Row{
			BlindChoice: ChoiceNoSuite,
			Reason: fmt.Sprintf("no promptfoo suite: %s is not a marketplace agent definition (plugins/<pack>/agents/<name>.md)",
				orDash(target)),
		})
	}
	ws, err := prepare(ctx, s.Source, p)
	if err != nil {
		return 0, err
	}
	defer ws.cleanup()

	cases, reason, err := loadSuite(ws.base, target)
	if err != nil {
		return 0, err
	}
	if len(cases) == 0 {
		return 0, s.insert(jobID, p.ID, Row{BlindChoice: ChoiceNoSuite, Reason: reason})
	}

	maxUSD := s.maxUSD()
	spent := 0.0
	for i, c := range cases {
		if spent >= maxUSD {
			for _, rest := range cases[i:] {
				if err := s.insert(jobID, p.ID, Row{
					TaskID: rest.ID, BlindChoice: ChoiceCapped,
					Reason: fmt.Sprintf("cost cap reached: $%.2f spent of $%.2f (%s)", spent, maxUSD, MaxUSDEnv),
				}); err != nil {
					return spent, err
				}
			}
			return spent, nil
		}
		row, usd, err := s.runCase(ctx, ws, c, spent, maxUSD)
		spent += usd
		if ierr := s.insert(jobID, p.ID, row); ierr != nil {
			return spent, ierr
		}
		if err != nil {
			// An infrastructure failure (promptfoo or claude missing, a crash) is
			// not a case result and would repeat on every remaining case: stop.
			return spent, err
		}
	}
	return spent, nil
}

// runCase runs one case on both sides and, on a tie of the deterministic
// result, asks the blind judge. The returned row is always insertable; a
// non-nil error means the job should stop.
func (s *Service) runCase(ctx context.Context, ws workspace, c Case, spent, maxUSD float64) (Row, float64, error) {
	row := Row{TaskID: c.ID}
	b, err := s.Suite.RunCase(ctx, ws.base, c)
	if err != nil {
		row.BlindChoice, row.Reason = ChoiceSkipped, "base run failed: "+clip(err.Error(), 400)
		return row, 0, err
	}
	v, err := s.Suite.RunCase(ctx, ws.variant, c)
	usd := b.USD + v.USD
	if err != nil {
		row.BlindChoice, row.Reason, row.USD = ChoiceSkipped, "variant run failed: "+clip(err.Error(), 400), usd
		return row, usd, err
	}
	row.BasePass, row.VariantPass = &b.HardPass, &v.HardPass
	rubric := rubricLine(b, v)

	switch {
	case b.HardPass != v.HardPass:
		row.BlindChoice = ChoiceSkipped
		row.Reason = "hard assertions disagree, so the deterministic result decides; no judge"
	case spent+usd >= maxUSD:
		row.BlindChoice = ChoiceCapped
		row.Reason = fmt.Sprintf("cost cap reached before the blind judge: $%.2f of $%.2f", spent+usd, maxUSD)
	default:
		choice, reason, jusd := s.judge(ctx, b, v)
		usd += jusd
		row.BlindChoice, row.Reason = choice, reason
	}
	if rubric != "" {
		row.Reason = strings.TrimSpace(row.Reason + "\n" + rubric)
	}
	row.USD = usd
	return row, usd, nil
}

// judge shuffles the two outputs into A and B, asks the blind judge, and maps
// the answer back. A judge failure is a skipped row, never a job failure.
func (s *Service) judge(ctx context.Context, b, v CaseRun) (choice, reason string, usd float64) {
	baseAtA := s.flip()
	a, bb := v.Output, b.Output
	if baseAtA {
		a, bb = b.Output, v.Output
	}
	task := b.Task
	if task == "" {
		task = v.Task
	}
	raw, usd, err := s.Judge.Judge(ctx, RenderJudgePrompt(task, a, bb))
	if err != nil {
		return ChoiceSkipped, "blind judge failed: " + clip(err.Error(), 400), usd
	}
	pick, why, err := ParseVerdict(raw)
	if err != nil {
		return ChoiceSkipped, "blind judge answer unparseable: " + clip(err.Error(), 200), usd
	}
	// The judge's reasoning names A and B; the operator needs the key, which
	// only this side of the prompt knows.
	key := "[A=variant · B=base] "
	if baseAtA {
		key = "[A=base · B=variant] "
	}
	why = key + why
	switch {
	case pick == "TIE":
		return ChoiceTie, why, usd
	case (pick == "A") == baseAtA:
		return ChoiceBase, why, usd
	default:
		return ChoiceVariant, why, usd
	}
}

// rubricLine records the model-graded assertions without letting them decide.
func rubricLine(b, v CaseRun) string {
	if b.SoftTotal == 0 && v.SoftTotal == 0 {
		return ""
	}
	return fmt.Sprintf("llm-rubric (advisory): base %d/%d · variant %d/%d",
		b.SoftPassed, b.SoftTotal, v.SoftPassed, v.SoftTotal)
}

// isAgentFile matches plugins/<pack>/agents/<name>.md — the only target with a
// promptfoo suite (a SKILL.md has none).
func isAgentFile(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 4 && parts[0] == "plugins" && parts[1] != "" && parts[2] == "agents" &&
		strings.HasSuffix(parts[3], ".md") && len(parts[3]) > len(".md") && path.Clean(p) == p
}

func orDash(s string) string {
	if s == "" {
		return "(no target)"
	}
	return s
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
