package verify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procfind"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// tsFormat matches the millisecond-Z style the api/dispatch packages write.
const tsFormat = "2006-01-02T15:04:05.000Z"

// Trees is the git boundary the service needs: the tree fingerprint for the
// cache and (reserved) the task's commits. Satisfied by *worktree.Manager. An
// interface so unit tests stub git with no process spawned, and so `verify`
// depends on `worktree`+`store` ONLY — never on `dispatch` (the trigger seam is
// an interface OWNED by dispatch that verify.Service satisfies; see Poke).
type Trees interface {
	TreeHash(worktreePath string) (string, error)
	// DiffFileCount reports how many files the worktree's work touches against
	// base. It is the pre-flight scope signal: a change beyond a size a bounded
	// read-only pass can conclude on is refused BEFORE a session is spent on it,
	// rather than after the hard timeout kills it. An error must not be read as
	// zero — see the scope gate, which skips the check instead of guessing.
	DiffFileCount(worktreePath, base string) (int, error)
}

// Service owns verification: single-flight per task, tree-hash cache gate, the
// read-only run + verdict parse + stamp, fix-task creation with root-inherited
// retry budget, the stale-run reaper, and startup heal. Async execution is the
// caller's job (api.spawnVerify / a goroutine), mirroring internal/improve and
// internal/dispatch — Poke is non-blocking, VerifyTask blocks.
type Service struct {
	DB    *sql.DB
	Cfg   Config
	Run   Runner
	Trees Trees
	UUID  func() string    // verifier session-uuid generator (test seam)
	now   func() time.Time // clock (test seam)
	Go    func(func())     // async-spawn seam (nil ⇒ real `go`), mirrors dispatch.Go
	// Notify emits task_updated (wired to api.publishTaskUpdated) so a stamped
	// verdict reaches the board over the FROZEN WS bus — no new message type.
	Notify func(taskID int64)
	// NotifyPlan emits plan_updated for one epic (wired to the same publisher
	// phaserun uses), so a PHASE verdict reaches the Plans page over that same frozen
	// bus. A separate seam from Notify rather than a shared one: the two surfaces
	// subscribe to different messages, and the ids are from different tables — a
	// phase's epic id sent as a board task id would refresh an unrelated card.
	// nil ⇒ no nudge.
	NotifyPlan func(workspaceTaskID int64)
	// PlaybookVerify resolves a task's verify strictness knob (fusion phase 13)
	// from its playbook name + project path: "strict" | "normal" | "off". nil ⇒
	// every task verifies at the normal bar (pre-playbook behavior; keeps verify
	// unit tests hermetic). Wired at the composition root from the playbook
	// registry so `verify` stays decoupled from `playbooks`. "off" skips the run
	// entirely (no verdict stamped — the task keeps whatever verdict it had).
	PlaybookVerify func(projectPath, playbookName string) string
	// FindRun locates a verification process that outlived the daemon, by its
	// session uuid. nil ⇒ a ps scan (procfind). Test seam.
	FindRun func(sessionUUID string) (int, bool)

	sem chan struct{} // verification concurrency cap (Cfg.Concurrency)
}

// NewService builds a verifier. The caller wires DB, Cfg, Run (ClaudeRunner),
// Trees (worktree.Manager), and Notify; UUID/now/Go default to production impls.
func NewService(db *sql.DB, cfg Config, r Runner, trees Trees) *Service {
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = DefaultConcurrency
	}
	if cfg.RetryBudget <= 0 {
		cfg.RetryBudget = DefaultRetryBudget
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	return &Service{
		DB: db, Cfg: cfg, Run: r, Trees: trees,
		UUID: runcore.NewUUID,
		now:  time.Now,
		sem:  make(chan struct{}, conc),
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
func (s *Service) ts() string { return s.clock().UTC().Format(tsFormat) }

func (s *Service) notify(id int64) {
	if s.Notify != nil {
		s.Notify(id)
	}
}

// spawn runs fn asynchronously with panic-recover (a verification goroutine must
// never take the daemon down — mirrors dispatch.spawn / spawnImprove). The Go
// seam (nil in production) lets tests run inline for determinism.
func (s *Service) spawn(fn func()) {
	wrapped := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: verify: goroutine panic recovered: %v", r)
			}
		}()
		fn()
	}
	if s.Go != nil {
		s.Go(wrapped)
		return
	}
	go wrapped()
}

// ── public API ──

// Poke is the AUTO trigger the dispatcher calls when a dispatched task lands
// in_review WITHOUT a sentinel. It is the method that satisfies dispatch's
// Verifier seam interface. Kill-switch honored HERE (auto only) so the manual
// endpoint can still force a run when SWARMERY_AUTOVERIFY=0. Non-blocking: it
// spawns VerifyTask on a goroutine (verification takes minutes).
func (s *Service) Poke(taskID int64) {
	if !s.Cfg.Enabled {
		return // auto-verify disabled; manual endpoint still routes to VerifyTask
	}
	s.spawn(func() {
		if err := s.VerifyTask(context.Background(), taskID); err != nil {
			log.Printf("error: verify: auto VerifyTask(%d): %v", taskID, err)
		}
	})
}

// ErrNoWorktree is returned by VerifyTask when the task has no live worktree to
// grade (never dispatched, or already reclaimed). The manual endpoint maps it to
// 422.
var ErrNoWorktree = errors.New("verify: task has no worktree to grade")

// ErrAlreadyRunning is returned when a verification is already in flight for the
// task (single-flight). The manual endpoint maps it to 409.
var ErrAlreadyRunning = errors.New("verify: a verification is already running for this task")

// VerifyTask verifies one board card. It is a Target CONSTRUCTOR over
// VerifyTarget — the row load, the playbook knob, the stamp destination (tasks) and
// the fix-task chain are the task-shaped parts; everything below the Target is the
// same engine a phase run goes through.
//
// BLOCKS — the caller runs it in a goroutine (Poke) or inline for the manual
// endpoint's async seam. All infra failures degrade to INCONCLUSIVE (never FAIL — an
// env problem is not evidence the work is wrong; DESIGN.md §4.6), so a fix task is
// spawned ONLY on a genuine FAIL verdict.
func (s *Service) VerifyTask(ctx context.Context, taskID int64) error {
	// Step 1 gate: task exists (the one lookup the engine cannot do — it holds no
	// notion of a task).
	tk, err := s.loadTask(taskID)
	if err != nil {
		return err
	}

	// Step 1 gate: the playbook verify knob (fusion phase 13). A task whose
	// playbook sets verify:off is never graded — no run row, no verdict stamp
	// (the task keeps whatever verdict it had). strict/normal fall through and
	// tighten the prompt bar in Step 3. Resolved via the injected seam so verify
	// stays decoupled from the playbook registry; nil seam ⇒ everything is normal.
	// The Target carries it; the engine enforces it.
	return s.VerifyTarget(ctx, Target{
		Key:          TaskKey(taskID),
		TaskID:       taskID,
		WorktreePath: tk.worktreePath,
		Branch:       tk.branch,
		StartPoint:   tk.startPoint,
		Title:        tk.title,
		Prompt:       tk.prompt,
		Model:        tk.model,
		ProjectPath:  tk.projectPath,
		Strictness:   s.strictness(tk),
		Stamp: func(v Verdict, detail string) error {
			_, err := s.DB.Exec(
				`UPDATE tasks SET verify_verdict=?, verify_detail=NULLIF(?, '') WHERE id=?`,
				string(v), detail, taskID)
			return err
		},
		// The fix-task chain is board-only. tk is captured rather than reloaded: the
		// budget it carries (verify_retry_count) can only be moved by another
		// verification of this same task, which single-flight has already excluded.
		OnFail: func(detail string) error { return s.handleFail(tk, detail) },
		Notify: func() { s.notify(taskID) },
	})
}

// VerifyTarget is the verification ENGINE, identical for every surface: gate,
// single-flight, tree-hash memo, scope gate, the read-only run, verdict parse, stamp
// (spec steps 1-6). It performs no lookups of its own — everything it grades and
// everywhere it writes arrives in the Target, which is what lets a phase run reuse it
// without a single task-shaped branch.
//
// BLOCKS. Every infra failure degrades to INCONCLUSIVE, never FAIL.
func (s *Service) VerifyTarget(ctx context.Context, t Target) error {
	// Gate: something to grade. An absent worktree is not a failing one.
	if strings.TrimSpace(t.WorktreePath) == "" {
		return ErrNoWorktree
	}
	if t.Stamp == nil {
		// A target with nowhere to record its answer would spend a session to produce
		// a verdict nothing keeps. Programmer error, refused before the run row.
		return fmt.Errorf("verify: target %s has no Stamp", t.Key)
	}

	// Gate: the strictness knob. `off` is never graded — no run row, no verdict stamp
	// (the target keeps whatever verdict it had). It is the board's playbook verify:off
	// and the phase doc's `**Verify:** off` alike, resolved by the constructor above.
	if t.Strictness == strictnessOff {
		return nil
	}

	// Gate: single-flight. Insert a `running` row; the partial unique index
	// (idx_verification_running) rejects a second in-flight run for the same TARGET
	// KEY, so this INSERT IS the lock (durable, survives restart — the reaper/heal
	// reclaim a stuck one). Mirrors provision.Enqueue's index guard.
	runID, err := s.beginRun(t)
	if errors.Is(err, ErrAlreadyRunning) {
		return ErrAlreadyRunning
	}
	if err != nil {
		return err
	}

	// From here every exit MUST finalize the run row (finishRun) so no `running`
	// row leaks (a leak would block all future verifies of this target until the
	// reaper fires). Concurrency cap around the actual work.
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	// Step 2: tree-hash gate. A cache hit for (tree_hash, target_key) stamps the
	// cached verdict and records a detail='cache' run WITHOUT spawning. A
	// tree-hash error (worktree vanished mid-flight — the RemoveWorktreeFor race)
	// degrades to INCONCLUSIVE, not FAIL.
	treeHash, err := s.Trees.TreeHash(t.WorktreePath)
	if err != nil {
		return s.stampInconclusive(t, runID, "", ClassUnverifiable, "could not read worktree tree ("+err.Error()+"): worktree may have been reclaimed")
	}
	if cached, ok, cerr := s.cacheGet(treeHash, t.Key); cerr != nil {
		return cerr
	} else if ok {
		return s.stampCached(t, runID, treeHash, cached)
	}

	// Step 2.5: scope gate. A change too large for a bounded read-only pass is
	// refused BEFORE spawning — the alternative is a full RunTimeout burned on a run
	// that ends INCONCLUSIVE anyway, which is the same answer at maximum cost. This
	// is the only enforceable scope bound available here: verification does not run
	// commands itself (runner.go spawns `claude` and the model chooses what to run),
	// so nothing in Go can cap the commands — only whether the session happens.
	//
	// A git failure SKIPS the gate rather than blocking: an unreadable diff is not
	// evidence of a huge one, and refusing on it would deny verification for a repo
	// state we simply could not measure.
	//
	// The base is the target's StartPoint: the SHA the run's worktree was pinned to
	// (tasks.start_point / epic_phases.run_start_point). It used to be the branch —
	// the branch diffed against ITSELF, which is always zero files, so the gate could
	// never fire and the prompt's "diff vs" instruction named a no-op range. Both
	// consumers now measure the same real interval, base...HEAD.
	base := t.StartPoint
	if base == "" {
		// Row from before the start point was recorded — no honest base exists. Skip
		// the scope gate (an unmeasurable diff is not evidence of a huge one; and
		// re-introducing the branch here would gate on a guaranteed-zero diff, which
		// is worse than not gating: it looks like a check and can never fail) and let
		// the prompt fall back to the branch name as before.
		base = t.Branch
		log.Printf("verify: %s: no start point recorded, diff base falls back to branch", t.Key)
	}
	if s.Cfg.MaxDiffFiles > 0 && t.StartPoint != "" {
		if n, derr := s.Trees.DiffFileCount(t.WorktreePath, base); derr != nil {
			log.Printf("verify: %s: diff size unreadable, scope gate skipped: %v", t.Key, derr)
		} else if n > s.Cfg.MaxDiffFiles {
			return s.stampInconclusive(t, runID, treeHash, ClassUnverifiable, fmt.Sprintf(
				"diff spans %d files, above the %d-file bound for a bounded read-only pass: "+
					"split the work or raise SWARMERY_VERIFY_MAX_DIFF_FILES", n, s.Cfg.MaxDiffFiles))
		}
	}

	// Step 3: run the read-only verifier + parse the verdict.
	uuid := s.UUID()
	s.linkVerifySession(runID, uuid)
	model := t.Model
	if model == "" {
		model = DefaultModel
	}
	spec := RunSpec{
		// base, not the branch: BuildPrompt's third parameter has always been named
		// startPoint (prompt.go) — we are finally passing what it asked for.
		Prompt:      BuildPrompt(t.Title, t.Prompt, base, t.Strictness),
		SessionUUID: uuid,
		Cwd:         t.WorktreePath,
		Model:       model,
		// Resolved from the PROJECT path, never from Cwd: Cwd is the run's
		// worktree and carries no .claude/settings.local.json, so a cwd-side
		// resolve would silently verify under the default account (plan A3).
		// "" = unbound project = default account = no env delta.
		Account: claudeacct.Binding(t.ProjectPath),
	}
	run, rerr := s.Run.Run(ctx, spec)
	if rerr != nil {
		// Process never ran (PATH miss/fork failure) → INCONCLUSIVE.
		return s.stampInconclusive(t, runID, treeHash, ClassNotStarted, "the verifier process never ran, so nothing about this work was measured: "+rerr.Error())
	}
	if run.TimedOut {
		// Killed by the hard timeout → INCONCLUSIVE (could not conclude), never FAIL.
		return s.stampInconclusive(t, runID, treeHash, ClassTimedOut, fmt.Sprintf("killed by the %s hard timeout before it reported a verdict", s.Cfg.RunTimeout))
	}

	verdict, reasons := ParseVerdict(run.Output)

	// Step 4 + 5: stamp the verdict, cache pass/fail (never inconclusive), and on
	// FAIL hand off to the target's own fail follow-up (the board's fix-task chain;
	// a nil hook — every phase target — stamps and stops).
	switch verdict {
	case VerdictPass:
		return s.stampVerdict(t, runID, treeHash, VerdictPass, reasons, true /*cache*/)
	case VerdictFail:
		if err := s.stampVerdict(t, runID, treeHash, VerdictFail, reasons, true /*cache*/); err != nil {
			return err
		}
		return t.onFail(reasons)
	default: // VerdictInconclusive
		// Two different events land here, and the operator has to be able to tell
		// them apart: a verifier that ran and declined to call it, versus one that
		// exited without saying anything at all (the 1.1s-and-blank rows in the
		// store). ParseVerdict returns the same fail-safe verdict for both, so the
		// distinction has to be made from the output itself.
		if strings.TrimSpace(run.Output) == "" {
			return s.stampInconclusive(t, runID, treeHash, ClassNoVerdict, fmt.Sprintf(
				"the verifier exited (code %d) having written nothing: no transcript, no verdict line. "+
					"This is a spawn or start-up failure, not an undecided verdict", run.ExitCode))
		}
		if !HasVerdictLine(run.Output) {
			return s.stampInconclusive(t, runID, treeHash, ClassNoVerdict, fmt.Sprintf(
				"the verifier wrote %d bytes (exit code %d) but no VERDICT: line, so nothing could be read as a verdict",
				len(run.Output), run.ExitCode))
		}
		return s.stampInconclusive(t, runID, treeHash, ClassCouldNotConclude, reasons)
	}
}

// VerifyPhase grades a finished phase run — the second target this engine serves
// (§5.3). It satisfies runcore.PhaseVerifier, which phaserun calls from its run
// goroutine while the worktree is still on disk.
//
// The verdict is an INPUT to the phase's diagnosis, never a second status: it lands
// on epic_phases.verify_verdict, where phasediag turns a `fail` into a verify-failed
// blocker beside a checkbox-derived outcome (decision D5). No fix task is spawned —
// OnFail is deliberately absent, because turning a failed plan phase into more
// dispatched work is the operator's call on the Plans page, not the verifier's.
func (s *Service) VerifyPhase(ctx context.Context, req runcore.PhaseVerifyRequest) error {
	return s.VerifyTarget(ctx, Target{
		Key:          PhaseKey(req.PhaseID),
		WorktreePath: req.WorktreePath,
		Branch:       req.Branch,
		StartPoint:   req.StartPoint,
		Title:        req.Title,
		Prompt:       req.Prompt,
		ProjectPath:  req.ProjectPath,
		Strictness:   StrictnessFromMode(req.Mode),
		Stamp: func(v Verdict, detail string) error {
			_, err := s.DB.Exec(
				`UPDATE epic_phases SET verify_verdict=?, verify_detail=NULLIF(?, '') WHERE id=?`,
				string(v), detail, req.PhaseID)
			return err
		},
		Notify: func() { s.notifyPlan(req.WorkspaceTaskID) },
	})
}

// StrictnessFromMode maps a phase doc's `**Verify:**` mode (wsingest's off|normal|
// strict) onto the prompt bar. Anything unrecognized is `off` — the same direction
// wsingest's parser already normalizes in, because running a grader nobody asked for
// is worse than not running one.
func StrictnessFromMode(mode string) Strictness {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "strict":
		return StrictnessStrict
	case "normal":
		return StrictnessNormal
	default:
		return strictnessOff
	}
}

// strictnessOff is the internal marker for a task whose playbook disables
// verification (verify:off). It is NOT a valid BuildPrompt Strictness — the
// service returns before building a prompt when it resolves.
const strictnessOff Strictness = "off"

// strictness resolves a task's verify knob through the injected PlaybookVerify
// seam: "strict" | "normal" | "off". A nil seam, an unresolved playbook, or any
// unrecognized value defaults to normal (pre-playbook behavior). This is the one
// place the phase-13 knob is read for auto AND manual verification.
func (s *Service) strictness(tk task) Strictness {
	if s.PlaybookVerify == nil {
		return StrictnessNormal
	}
	switch s.PlaybookVerify(tk.projectPath, tk.playbook) {
	case "off":
		return strictnessOff
	case "strict":
		return StrictnessStrict
	default:
		return StrictnessNormal
	}
}

// ── task load ──

type task struct {
	id           int64
	externalID   string
	projectID    int64
	title        string
	prompt       string
	model        string
	origin       string // 'manual' | 'session' | 'llm' | 'verify-fix' (fix-chain marker)
	branch       string
	worktreePath string
	// startPoint is the SHA admit() pinned the worktree to (0051). "" on a row
	// dispatched before that migration — there is no honest base for those, and
	// the consumers below must fall back rather than invent one.
	startPoint string
	fileScope  string // raw JSON, copied verbatim to a fix task
	// verifyRetryCount is the fix-chain budget. Deliberately NOT tasks.retry_count:
	// that one is the dispatcher's no-progress heal budget, and verification has no
	// business reading or spending it. The struct carries only the counter this
	// package owns so the split cannot be undone by accident.
	verifyRetryCount int
	playbook         string // selected recipe name ("" ⇒ default); drives the verify knob
	projectPath      string // repo root, for the playbook registry lookup
}

func (s *Service) loadTask(id int64) (task, error) {
	var t task
	var extID, model, branch, wtpath, playbook sql.NullString
	err := s.DB.QueryRow(`
		SELECT t.id, COALESCE(t.external_id,''), t.project_id, t.title, t.prompt, t.model,
		       t.origin, t.branch, t.worktree_path, COALESCE(t.start_point,''), t.file_scope,
		       t.verify_retry_count, t.playbook, p.path
		  FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id=?`, id).
		Scan(&t.id, &extID, &t.projectID, &t.title, &t.prompt, &model,
			&t.origin, &branch, &wtpath, &t.startPoint, &t.fileScope,
			&t.verifyRetryCount, &playbook, &t.projectPath)
	if errors.Is(err, sql.ErrNoRows) {
		return task{}, fmt.Errorf("verify: task %d not found", id)
	}
	if err != nil {
		return task{}, err
	}
	t.externalID = extID.String
	t.model = model.String
	t.branch = branch.String
	t.worktreePath = wtpath.String
	t.playbook = playbook.String
	return t, nil
}

// ── verification_runs lifecycle (single-writer inline SQL) ──

// beginRun inserts a `running` row for the target, returning its id. A unique-index
// violation (idx_verification_running, now keyed on target_key — 0058) means another
// run is in flight → ErrAlreadyRunning.
func (s *Service) beginRun(t Target) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO verification_runs(target_key, task_id, status, started_at)
		 VALUES(?, ?, 'running', ?)`,
		t.Key, t.taskIDValue(), s.ts())
	if err != nil {
		// The partial unique index rejected a second in-flight row.
		var existing int64
		if again := s.DB.QueryRow(
			`SELECT id FROM verification_runs WHERE target_key=? AND status='running' LIMIT 1`,
			t.Key).Scan(&existing); again == nil {
			return 0, ErrAlreadyRunning
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// finishRun stamps a terminal status + detail + tree_hash + finished_at on a run
// row. detail is truncated to the schema's 4KB budget.
func (s *Service) finishRun(runID int64, status string, treeHash, detail string) {
	if _, err := s.DB.Exec(`
		UPDATE verification_runs
		   SET status=?, tree_hash=NULLIF(?, ''), detail=NULLIF(?, ''), finished_at=?
		 WHERE id=?`,
		status, treeHash, truncate(detail, verdictReasonsCap), s.ts(), runID); err != nil {
		log.Printf("error: verify: finish run %d: %v", runID, err)
	}
}

// linkVerifySession parks the verifier's own headless session uuid on the run
// row (the explicit link, reconciled to a sessions row by ingest later — same
// spirit as dispatch's dispatch_session_uuid). Best-effort.
func (s *Service) linkVerifySession(runID int64, uuid string) {
	if _, err := s.DB.Exec(
		`UPDATE verification_runs SET verify_session_uuid=? WHERE id=?`, uuid, runID); err != nil {
		log.Printf("error: verify: link verify session (run %d): %v", runID, err)
	}
}

// ── stamping ──

// stampVerdict writes the verdict + detail onto the target's own row (Target.Stamp:
// tasks for a card, epic_phases for a phase), finalizes the run row, optionally
// caches a pass/fail verdict, and nudges the target's live surface. It NEVER caches
// inconclusive (guarded by the caller passing cache=false for that path).
//
// The detail is truncated HERE, once, before it reaches any Stamp — the 4KB budget is
// the schema's, not the board's, and a second stamper that forgot the cap would park
// an unbounded model transcript in a column.
func (s *Service) stampVerdict(t Target, runID int64, treeHash string, v Verdict, detail string, cache bool) error {
	if err := t.Stamp(v, truncate(detail, verdictReasonsCap)); err != nil {
		return err
	}
	s.finishRun(runID, string(v), treeHash, detail)
	if cache && treeHash != "" && (v == VerdictPass || v == VerdictFail) {
		s.cachePut(treeHash, t.Key, v)
	}
	t.notify()
	return nil
}

// Inconclusive CLASSES. Every infra path degrades to INCONCLUSIVE — correctly,
// because none of them is evidence of a real failure — but "the verifier never
// started" and "the verifier ran and could not conclude" are different
// operational events with different fixes, and they used to arrive as one
// undifferentiated detail string (sometimes an EMPTY one, when a run produced no
// parseable verdict and no reasons: the store holds such a row, 1.1s long, with
// nothing in `detail` at all).
//
// So each path is labelled at the point it is taken. The prefix is stable and
// greppable so the operator, the dashboard and a later query all read the same
// classification instead of re-deriving it from prose.
const (
	// ClassNotStarted — the process never ran: PATH miss, fork failure, missing
	// binary. Nothing about the work was measured. Fix the environment.
	ClassNotStarted = "verifier-did-not-start"
	// ClassTimedOut — started, killed by the hard timeout. Partial work, no verdict.
	ClassTimedOut = "verifier-timed-out"
	// ClassNoVerdict — started, exited, and produced no verdict line. This is the
	// class that used to be invisible: an empty-output run and a genuinely
	// ambiguous one were both stamped with whatever reasons happened to parse.
	ClassNoVerdict = "verifier-produced-no-verdict"
	// ClassUnverifiable — the run was never attempted because its input could not
	// be read or was out of scope (worktree reclaimed, diff too large).
	ClassUnverifiable = "not-verifiable"
	// ClassCouldNotConclude — the verifier ran, reported, and declined to call it.
	// The only class that is a statement about the WORK rather than the machinery.
	ClassCouldNotConclude = "could-not-conclude"
)

// stampInconclusive is the fail-safe stamp: verdict inconclusive, run finalized,
// NOTHING cached, NO fail follow-up. Used for every infra/ambiguity path.
//
// class is one of the Class* constants above and is REQUIRED — an unclassified
// inconclusive is what made a dead verifier look like an undecided one.
func (s *Service) stampInconclusive(t Target, runID int64, treeHash, class, detail string) error {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		// Never stamp an empty detail. A blank cell reads as "nothing happened",
		// which is the one thing it never means.
		detail = "no detail reported by the verifier"
	}
	return s.stampVerdict(t, runID, treeHash, VerdictInconclusive, class+": "+detail, false /*never cache*/)
}

// stampCached stamps a cache-hit verdict without spawning: it finalizes the run
// row with detail='cache' and stamps the target with the memoized verdict. The
// cache only ever holds pass/fail, so this never produces inconclusive.
func (s *Service) stampCached(t Target, runID int64, treeHash string, v Verdict) error {
	if err := t.Stamp(v, "verified from cache (unchanged tree)"); err != nil {
		return err
	}
	s.finishRun(runID, string(v), treeHash, "cache")
	t.notify()
	// A cached FAIL still needs the fail follow-up (the tree is unchanged and still
	// failing).
	if v == VerdictFail {
		return t.onFail("verification failed (unchanged tree, cached verdict)")
	}
	return nil
}

// ── tree-hash cache (single-writer inline SQL) ──

func (s *Service) cacheGet(treeHash, targetKey string) (Verdict, bool, error) {
	var v string
	err := s.DB.QueryRow(
		`SELECT verdict FROM verification_cache WHERE tree_hash=? AND target_key=?`,
		treeHash, targetKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Verdict(v), true, nil
}

// cachePut memoizes a pass/fail verdict for (tree_hash, target_key). INSERT OR
// IGNORE: a concurrent identical put is harmless. Never called for inconclusive
// (the CHECK constraint would also reject it). Best-effort — a cache write
// failure must not fail the verdict.
func (s *Service) cachePut(treeHash, targetKey string, v Verdict) {
	if v != VerdictPass && v != VerdictFail {
		return
	}
	if _, err := s.DB.Exec(
		`INSERT OR IGNORE INTO verification_cache(tree_hash, target_key, verdict, created_at)
		 VALUES(?,?,?,?)`, treeHash, targetKey, string(v), s.ts()); err != nil {
		log.Printf("error: verify: cache put (%s): %v", targetKey, err)
	}
}

// ── fail → fix task (root-inherited retry budget, dedup) ──

// handleFail implements spec steps 5-6 for a FAIL verdict: walk the fix-chain to
// the ROOT task, charge the ROOT's retry budget, and either create ONE deduped
// fix task (todo) or, at budget exhaustion, pause the chain.
func (s *Service) handleFail(current task, reasons string) error {
	root, err := s.resolveRoot(current)
	if err != nil {
		return err
	}

	// Budget check against the ROOT's verify_retry_count (root-inherited budget).
	// At or over budget → pause root + current, no new fix task.
	//
	// This is the VERIFY budget, not tasks.retry_count (0051). They were one
	// column until the split, so a task the dispatcher's no-progress heal had
	// requeued a few times arrived here with its fix budget already spent — the
	// two budgets bound unrelated things and now count separately.
	if root.verifyRetryCount >= s.Cfg.RetryBudget {
		return s.pauseExhausted(root.id, current.id)
	}

	// Dedup (Fusion R22): an existing NON-terminal fix task for this root → reuse.
	// The task being verified RIGHT NOW is excluded — a fix task that fails its
	// own verification must not see ITSELF as the blocking open fix (that would
	// wedge the chain: the current fix never terminates during its own grade).
	if exists, err := s.hasOpenFix(root.externalID, current.id); err != nil {
		return err
	} else if exists {
		log.Printf("verify: task %d failed; open fix task for root %s already exists — not creating another", current.id, root.externalID)
		return nil
	}

	// Charge the ROOT and create the fix task in todo.
	if err := s.incrementRetry(root.id); err != nil {
		return err
	}
	return s.createFixTask(root, reasons)
}

// resolveRoot follows the fix chain from `current` to its origin. A fix task
// carries origin='verify-fix' and external_id=<root task id>; walk until a task
// whose origin is NOT 'verify-fix' (the human/queue-created root). Cycles are
// impossible (each fix points at the fixed task's id, strictly older) but a
// bounded walk guards against a dangling pointer.
func (s *Service) resolveRoot(current task) (task, error) {
	cur := current
	for i := 0; i < 64; i++ {
		if cur.origin != "verify-fix" {
			return cur, nil
		}
		// external_id of a fix task is the id it is fixing (the root or a nearer
		// ancestor); load that task.
		parent, err := s.loadTaskByExternalID(cur.externalID)
		if err != nil {
			// Dangling chain: treat the current task as the root so budget still
			// applies (conservative — never spawn unbounded fixes).
			log.Printf("verify: fix-chain parent %q not found; treating task %d as root", cur.externalID, cur.id)
			return cur, nil
		}
		cur = parent
	}
	return cur, nil
}

func (s *Service) loadTaskByExternalID(extID string) (task, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM tasks WHERE external_id=? LIMIT 1`, extID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return task{}, fmt.Errorf("verify: task external_id %q not found", extID)
	}
	if err != nil {
		return task{}, err
	}
	return s.loadTask(id)
}

// hasOpenFix reports whether a non-terminal fix task already exists for a root
// (query by origin+external_id, board_column NOT IN done/archived), EXCLUDING
// excludeID — the task currently being verified, which must not count as its own
// dedup blocker. Dedup gate (Fusion R22).
func (s *Service) hasOpenFix(rootExternalID string, excludeID int64) (bool, error) {
	var one int
	err := s.DB.QueryRow(`
		SELECT 1 FROM tasks
		 WHERE origin='verify-fix' AND external_id=?
		   AND board_column NOT IN ('done','archived')
		   AND id<>?
		 LIMIT 1`, rootExternalID, excludeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// incrementRetry bumps the ROOT task's verify_retry_count (guarded to the row).
// tasks.retry_count is deliberately untouched: it is the dispatcher's
// no-progress heal budget (dispatch.HealDeadProcess) and charging it here would
// let a run of failed verifications park a task the dispatcher was willing to
// retry — the confusion 0051 split apart.
func (s *Service) incrementRetry(rootID int64) error {
	_, err := s.DB.Exec(`UPDATE tasks SET verify_retry_count=verify_retry_count+1 WHERE id=?`, rootID)
	return err
}

// createFixTask inserts a fix task in todo: prompt = root prompt + the
// verification-failed reasons; same file_scope + model as the root;
// external_id=<root external id> (so its own failure charges the root),
// dependencies=[]. Emits task_updated + is picked up by the dispatcher's own
// Poke (the api layer pokes after our return).
//
// source='queue', origin='verify-fix' — and which column carries the marker is
// the whole reason this task reaches a runner at all. `source` is not a label
// for who minted a row: it is the ownership axis the board and the dispatcher
// both key on ('queue' = a board row the dispatcher may own, 'workspace' = a
// projection of workspace artifacts that internal/wsingest rewrites on its next
// scan — see dispatch/service.go:1013). A fix task written with
// source='verify-fix' therefore matched NEITHER: listBoardTasks
// (api/tasks_board.go) and candidates() (dispatch/service.go) both select
// source='queue', so every fix task was minted straight into 'todo' and then
// orphaned — invisible in the UI and never dispatched, while the log line below
// and this function's own comment claimed the opposite.
//
// `origin` is the "who minted this card" axis ('manual' | 'session' | 'llm'),
// which is exactly what "the verifier made this one" is, so the fix-chain
// marker belongs there. It is also safe against capture's auto-move guards:
// moveCapturedToReview and SweepSessionToReview (ingest/capture.go) each pair
// their `origin != 'manual'` predicate with a capture-owned key (capture_key /
// origin_session_id) that is NULL on a fix task and can never match.
func (s *Service) createFixTask(root task, reasons string) error {
	fixPrompt := root.prompt + "\n\n## Verification failed\n" + strings.TrimSpace(reasons)
	var scope []string
	if err := json.Unmarshal([]byte(root.fileScope), &scope); err != nil {
		// A root whose file_scope is not a JSON array is a row written before
		// the column was normalized; the fix task falls back to the whole tree
		// rather than inheriting garbage.
		scope = nil
	}
	id, _, err := store.InsertBoardTask(s.DB, store.BoardTaskInput{
		ProjectID:  root.projectID,
		Title:      "fix: " + root.title,
		Prompt:     fixPrompt,
		Priority:   fixPriority,
		Column:     "todo",
		Origin:     "verify-fix",
		ExternalID: root.externalID,
		Model:      &root.model,
		FileScope:  scope,
		Now:        s.clock(),
	})
	if err != nil {
		return err
	}
	log.Printf("verify: created fix task %d for root %s (verify retry %d/%d)",
		id, root.externalID, root.verifyRetryCount+1, s.Cfg.RetryBudget)
	s.notify(id)
	return nil
}

// pauseExhausted pauses both the root and the current (failing) task with the
// budget-exhausted marker (spec step 5). paused=1 parks them; the user resumes
// after intervening.
func (s *Service) pauseExhausted(rootID, currentID int64) error {
	const marker = "verify retry budget exhausted"
	if _, err := s.DB.Exec(
		`UPDATE tasks SET paused=1, dispatch_error=? WHERE id IN (?, ?)`,
		marker, rootID, currentID); err != nil {
		return err
	}
	log.Printf("verify: retry budget exhausted for root %d (failing task %d) — chain paused", rootID, currentID)
	s.notify(rootID)
	if currentID != rootID {
		s.notify(currentID)
	}
	return nil
}

// fixPriority is the priority assigned to auto-created fix tasks: 'high' (3) so a
// regression fix is worked before fresh normal-priority backlog, but below an
// explicit 'urgent'. Matches api.priorityLabels.
const fixPriority = 3

// nullableModel maps an empty model string to NULL for storage (an empty TEXT
// override would otherwise be passed to `claude --model ""`).
func nullableModel(m string) any {
	if strings.TrimSpace(m) == "" {
		return nil
	}
	return m
}

// ── out-of-band stamping (reaper + startup heal) ──

// stampByKey writes a verdict onto whichever row a target key names, for the two
// paths that have NO Target: the reaper and the startup heal walk verification_runs
// rows, which outlive the Target that opened them. It is the one place the key ⇒
// table mapping is duplicated, and it is duplicated because the alternative —
// reconstructing a full Target from a run row — would have to invent the worktree
// path, the branch and the base that row no longer knows.
//
// onlyIfUnset preserves the heal's narrower contract: a restart must not overwrite a
// verdict a later run already reached, while the reaper deliberately does (the run it
// reaps IS the current one). An unroutable key is logged and skipped — guessing a
// kind would stamp a verdict onto an unrelated row.
func (s *Service) stampByKey(key string, v Verdict, detail string, onlyIfUnset bool) {
	kind, id, ok := SplitKey(key)
	if !ok {
		log.Printf("error: verify: cannot route verdict for unrecognized target key %q", key)
		return
	}
	guard := ""
	if onlyIfUnset {
		guard = ` AND (verify_verdict IS NULL OR verify_verdict='')`
	}
	table := "tasks"
	if kind == KindPhase {
		table = "epic_phases"
	}
	if _, err := s.DB.Exec(
		`UPDATE `+table+` SET verify_verdict=?, verify_detail=? WHERE id=?`+guard,
		string(v), detail, id); err != nil {
		log.Printf("error: verify: stamp %s: %v", key, err)
		return
	}
	switch kind {
	case KindTask:
		s.notify(id)
	case KindPhase:
		s.notifyPlanForPhase(id)
	}
}

// notifyPlan nudges the Plans page for one epic. Wired at the composition root to
// the same plan_updated publisher phaserun uses; nil ⇒ no nudge (the verdict is
// durable either way and shows on the next fetch).
func (s *Service) notifyPlan(workspaceTaskID int64) {
	if s.NotifyPlan != nil && workspaceTaskID > 0 {
		s.NotifyPlan(workspaceTaskID)
	}
}

// notifyPlanForPhase resolves a phase's epic and nudges it. Only the out-of-band
// paths need this lookup: VerifyPhase already receives the workspace task id from
// phaserun, which is holding the row.
func (s *Service) notifyPlanForPhase(phaseID int64) {
	if s.NotifyPlan == nil {
		return
	}
	var taskID int64
	if err := s.DB.QueryRow(
		`SELECT workspace_task_id FROM epic_phases WHERE id=?`, phaseID).Scan(&taskID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("error: verify: resolve epic of phase %d for notify: %v", phaseID, err)
		}
		return
	}
	s.notifyPlan(taskID)
}

// ── reaper + startup heal ──

// Reap marks `running` verification_runs rows older than Cfg.StaleAfter as
// `error` and stamps their task INCONCLUSIVE (a zombie verifier — killed
// process, wedged git — must not park the task forever, and an unconcluded run
// is inconclusive, never fail). Idempotent; safe to run on a ticker. Returns the
// number reaped.
func (s *Service) Reap() (int, error) {
	cutoff := s.clock().Add(-s.Cfg.StaleAfter).UTC().Format(tsFormat)
	rows, err := s.DB.Query(
		`SELECT id, target_key FROM verification_runs WHERE status='running' AND started_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type stuck struct {
		runID int64
		key   string
	}
	var stucks []stuck
	for rows.Next() {
		var st stuck
		if err := rows.Scan(&st.runID, &st.key); err != nil {
			rows.Close()
			return 0, err
		}
		stucks = append(stucks, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, st := range stucks {
		s.finishRun(st.runID, "error", "", "reaped: verification run exceeded "+s.Cfg.StaleAfter.String())
		s.stampByKey(st.key, VerdictInconclusive,
			"verification run stalled and was reaped", false /*overwrite*/)
	}
	if len(stucks) > 0 {
		log.Printf("swarmery verify: reaped %d stalled verification run(s)", len(stucks))
	}
	return len(stucks), nil
}

// HealStale reclaims `running` verification_runs rows a crashed/restarted daemon
// left behind. Mark it error + stamp the task inconclusive so it is re-verifiable
// (provision/dispatch heal idiom). Unlike Reap it ignores age (every running row
// at startup is by definition orphaned).
//
// A verifier process spawned in its own process group can OUTLIVE the daemon —
// but unlike a phase or plan run it must not be adopted: the verdict lives in the
// process's STDOUT, and the pipe that carried it died with the parent. Nobody can
// read that run's result any more, so a survivor is pure waste holding a worktree
// the operator may want to reuse. Each survivor is therefore killed (whole group,
// procgroup.Kill), which is also what makes the 'inconclusive' stamp true.
func (s *Service) HealStale() error {
	rows, err := s.DB.Query(
		`SELECT id, target_key, COALESCE(verify_session_uuid,'') FROM verification_runs WHERE status='running'`)
	if err != nil {
		return err
	}
	type stuck struct {
		runID     int64
		key, uuid string
	}
	var stucks []stuck
	for rows.Next() {
		var st stuck
		if err := rows.Scan(&st.runID, &st.key, &st.uuid); err != nil {
			rows.Close()
			return err
		}
		stucks = append(stucks, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, st := range stucks {
		s.killOrphan(st.uuid, st.runID)
		s.finishRun(st.runID, "error", "", "interrupted by daemon restart")
		s.stampByKey(st.key, VerdictInconclusive,
			"verification interrupted by daemon restart", true /*onlyIfUnset*/)
	}
	if len(stucks) > 0 {
		log.Printf("swarmery verify: healed %d interrupted verification run(s)", len(stucks))
	}
	return nil
}

// killOrphan terminates a verification process that outlived the daemon. Its
// verdict is unrecoverable (stdout died with the parent), so letting it run would
// burn tokens and hold a worktree for an answer nobody can read.
//
// FindRun is the test seam; nil ⇒ the ps scan. A miss is the normal case (the
// process really did die with the daemon) and is silent.
func (s *Service) killOrphan(sessionUUID string, runID int64) {
	find := s.FindRun
	if find == nil {
		find = procfind.BySessionUUID
	}
	pid, ok := find(sessionUUID)
	if !ok {
		return
	}
	log.Printf("verify: run %d outlived the daemon (pid=%d); killing it — its verdict is unreadable", runID, pid)
	if err := procgroup.Kill(pid); err != nil {
		log.Printf("warning: verify: kill orphaned run %d pid=%d: %v", runID, pid, err)
	}
}
