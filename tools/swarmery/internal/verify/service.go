package verify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
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
		UUID: newUUID,
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

// VerifyTask runs the whole flow for one task (spec steps 1-6). BLOCKS — the
// caller runs it in a goroutine (Poke) or inline for the manual endpoint's async
// seam. All infra failures degrade to INCONCLUSIVE (never FAIL — an env problem
// is not evidence the work is wrong; DESIGN.md §4.6), so a fix task is spawned
// ONLY on a genuine FAIL verdict.
func (s *Service) VerifyTask(ctx context.Context, taskID int64) error {
	// Step 1 gate: task exists + has a worktree.
	tk, err := s.loadTask(taskID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tk.worktreePath) == "" {
		return ErrNoWorktree
	}

	// Step 1 gate: single-flight. Insert a `running` row; the partial unique
	// index (idx_verification_running) rejects a second in-flight run for the
	// same task, so this INSERT IS the lock (durable, survives restart — the
	// reaper/heal reclaim a stuck one). Mirrors provision.Enqueue's index guard.
	runID, err := s.beginRun(taskID)
	if errors.Is(err, ErrAlreadyRunning) {
		return ErrAlreadyRunning
	}
	if err != nil {
		return err
	}

	// From here every exit MUST finalize the run row (finishRun) so no `running`
	// row leaks (a leak would block all future verifies of this task until the
	// reaper fires). Concurrency cap around the actual work.
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	// Step 2: tree-hash gate. A cache hit for (tree_hash, task_id) stamps the
	// cached verdict and records a detail='cache' run WITHOUT spawning. A
	// tree-hash error (worktree vanished mid-flight — the RemoveWorktreeFor race)
	// degrades to INCONCLUSIVE, not FAIL.
	treeHash, err := s.Trees.TreeHash(tk.worktreePath)
	if err != nil {
		return s.stampInconclusive(taskID, runID, "", "could not read worktree tree ("+err.Error()+"): worktree may have been reclaimed")
	}
	if cached, ok, cerr := s.cacheGet(treeHash, taskID); cerr != nil {
		return cerr
	} else if ok {
		return s.stampCached(taskID, runID, treeHash, cached)
	}

	// Step 3: run the read-only verifier + parse the verdict.
	uuid := s.UUID()
	s.linkVerifySession(runID, uuid)
	spec := RunSpec{
		Prompt:      BuildPrompt(tk.title, tk.prompt, tk.branch),
		SessionUUID: uuid,
		Cwd:         tk.worktreePath,
		Model:       tk.model,
	}
	run, rerr := s.Run.Run(ctx, spec)
	if rerr != nil {
		// Process never ran (PATH miss/fork failure) → INCONCLUSIVE.
		return s.stampInconclusive(taskID, runID, treeHash, "verifier did not run: "+rerr.Error())
	}
	if run.TimedOut {
		// Killed by the hard timeout → INCONCLUSIVE (could not conclude), never FAIL.
		return s.stampInconclusive(taskID, runID, treeHash, "verifier timed out")
	}

	verdict, reasons := ParseVerdict(run.Output)

	// Step 4 + 5: stamp the verdict, cache pass/fail (never inconclusive), and on
	// FAIL create/dedup a fix task within the root's retry budget.
	switch verdict {
	case VerdictPass:
		return s.stampVerdict(taskID, runID, treeHash, VerdictPass, reasons, true /*cache*/)
	case VerdictFail:
		if err := s.stampVerdict(taskID, runID, treeHash, VerdictFail, reasons, true /*cache*/); err != nil {
			return err
		}
		return s.handleFail(tk, reasons)
	default: // VerdictInconclusive
		return s.stampInconclusive(taskID, runID, treeHash, reasons)
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
	source       string
	branch       string
	worktreePath string
	fileScope    string // raw JSON, copied verbatim to a fix task
	retryCount   int
}

func (s *Service) loadTask(id int64) (task, error) {
	var t task
	var extID, model, branch, wtpath sql.NullString
	err := s.DB.QueryRow(`
		SELECT id, COALESCE(external_id,''), project_id, title, prompt, model,
		       source, branch, worktree_path, file_scope, retry_count
		  FROM tasks WHERE id=?`, id).
		Scan(&t.id, &extID, &t.projectID, &t.title, &t.prompt, &model,
			&t.source, &branch, &wtpath, &t.fileScope, &t.retryCount)
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
	return t, nil
}

// ── verification_runs lifecycle (single-writer inline SQL) ──

// beginRun inserts a `running` row, returning its id. A unique-index violation
// (idx_verification_running) means another run is in flight → ErrAlreadyRunning.
func (s *Service) beginRun(taskID int64) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO verification_runs(task_id, status, started_at) VALUES(?, 'running', ?)`,
		taskID, s.ts())
	if err != nil {
		// The partial unique index rejected a second in-flight row.
		var existing int64
		if again := s.DB.QueryRow(
			`SELECT id FROM verification_runs WHERE task_id=? AND status='running' LIMIT 1`,
			taskID).Scan(&existing); again == nil {
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

// stampVerdict writes tasks.verify_verdict + verify_detail, finalizes the run
// row, optionally caches a pass/fail verdict, and emits task_updated. It NEVER
// caches inconclusive (guarded by the caller passing cache=false for that path).
func (s *Service) stampVerdict(taskID, runID int64, treeHash string, v Verdict, detail string, cache bool) error {
	if _, err := s.DB.Exec(
		`UPDATE tasks SET verify_verdict=?, verify_detail=NULLIF(?, '') WHERE id=?`,
		string(v), truncate(detail, verdictReasonsCap), taskID); err != nil {
		return err
	}
	s.finishRun(runID, string(v), treeHash, detail)
	if cache && treeHash != "" && (v == VerdictPass || v == VerdictFail) {
		s.cachePut(treeHash, taskID, v)
	}
	s.notify(taskID)
	return nil
}

// stampInconclusive is the fail-safe stamp: verdict inconclusive, run finalized,
// NOTHING cached, NO fix task. Used for every infra/ambiguity path.
func (s *Service) stampInconclusive(taskID, runID int64, treeHash, detail string) error {
	return s.stampVerdict(taskID, runID, treeHash, VerdictInconclusive, detail, false /*never cache*/)
}

// stampCached stamps a cache-hit verdict without spawning: it finalizes the run
// row with detail='cache' and stamps the task with the memoized verdict. The
// cache only ever holds pass/fail, so this never produces inconclusive.
func (s *Service) stampCached(taskID, runID int64, treeHash string, v Verdict) error {
	if _, err := s.DB.Exec(
		`UPDATE tasks SET verify_verdict=?, verify_detail='verified from cache (unchanged tree)' WHERE id=?`,
		string(v), taskID); err != nil {
		return err
	}
	s.finishRun(runID, string(v), treeHash, "cache")
	s.notify(taskID)
	// A cached FAIL still needs the fix-task flow (the tree is unchanged and still
	// failing). Reload for the fix-chain walk.
	if v == VerdictFail {
		tk, err := s.loadTask(taskID)
		if err != nil {
			return err
		}
		return s.handleFail(tk, "verification failed (unchanged tree, cached verdict)")
	}
	return nil
}

// ── tree-hash cache (single-writer inline SQL) ──

func (s *Service) cacheGet(treeHash string, taskID int64) (Verdict, bool, error) {
	var v string
	err := s.DB.QueryRow(
		`SELECT verdict FROM verification_cache WHERE tree_hash=? AND task_id=?`,
		treeHash, taskID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Verdict(v), true, nil
}

// cachePut memoizes a pass/fail verdict for (tree_hash, task_id). INSERT OR
// IGNORE: a concurrent identical put is harmless. Never called for inconclusive
// (the CHECK constraint would also reject it). Best-effort — a cache write
// failure must not fail the verdict.
func (s *Service) cachePut(treeHash string, taskID int64, v Verdict) {
	if v != VerdictPass && v != VerdictFail {
		return
	}
	if _, err := s.DB.Exec(
		`INSERT OR IGNORE INTO verification_cache(tree_hash, task_id, verdict, created_at)
		 VALUES(?,?,?,?)`, treeHash, taskID, string(v), s.ts()); err != nil {
		log.Printf("error: verify: cache put (task %d): %v", taskID, err)
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

	// Budget check against the ROOT's retry_count (root-inherited budget). At or
	// over budget → pause root + current, no new fix task.
	if root.retryCount >= s.Cfg.RetryBudget {
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
// carries source='verify-fix' and external_id=<root task id>; walk until a task
// whose source is NOT 'verify-fix' (the human/queue-created root). Cycles are
// impossible (each fix points at the fixed task's id, strictly older) but a
// bounded walk guards against a dangling pointer.
func (s *Service) resolveRoot(current task) (task, error) {
	cur := current
	for i := 0; i < 64; i++ {
		if cur.source != "verify-fix" {
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
// (query by source+external_id, board_column NOT IN done/archived), EXCLUDING
// excludeID — the task currently being verified, which must not count as its own
// dedup blocker. Dedup gate (Fusion R22).
func (s *Service) hasOpenFix(rootExternalID string, excludeID int64) (bool, error) {
	var one int
	err := s.DB.QueryRow(`
		SELECT 1 FROM tasks
		 WHERE source='verify-fix' AND external_id=?
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

// incrementRetry bumps the ROOT task's retry_count (guarded to the row).
func (s *Service) incrementRetry(rootID int64) error {
	_, err := s.DB.Exec(`UPDATE tasks SET retry_count=retry_count+1 WHERE id=?`, rootID)
	return err
}

// createFixTask inserts a fix task in todo: prompt = root prompt + the
// verification-failed reasons; same file_scope + model as the root; source=
// 'verify-fix', external_id=<root external id> (so its own failure charges the
// root), dependencies=[]. Emits task_updated + would be picked up by the
// dispatcher's own Poke (the api layer pokes after our return).
func (s *Service) createFixTask(root task, reasons string) error {
	fixPrompt := root.prompt + "\n\n## Verification failed\n" + strings.TrimSpace(reasons)
	now := s.ts()
	res, err := s.DB.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, external_id, board_column, model, file_scope,
		                  dependencies, column_moved_at)
		VALUES(?, ?, ?, ?, 'queued', ?, 'verify-fix', ?, 'todo', ?, ?, '[]', ?)`,
		root.projectID, "fix: "+root.title, fixPrompt, fixPriority, now,
		root.externalID, nullableModel(root.model), root.fileScope, now)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	log.Printf("verify: created fix task %d for root %s (retry %d/%d)", id, root.externalID, root.retryCount+1, s.Cfg.RetryBudget)
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

// ── reaper + startup heal ──

// Reap marks `running` verification_runs rows older than Cfg.StaleAfter as
// `error` and stamps their task INCONCLUSIVE (a zombie verifier — killed
// process, wedged git — must not park the task forever, and an unconcluded run
// is inconclusive, never fail). Idempotent; safe to run on a ticker. Returns the
// number reaped.
func (s *Service) Reap() (int, error) {
	cutoff := s.clock().Add(-s.Cfg.StaleAfter).UTC().Format(tsFormat)
	rows, err := s.DB.Query(
		`SELECT id, task_id FROM verification_runs WHERE status='running' AND started_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type stuck struct{ runID, taskID int64 }
	var stucks []stuck
	for rows.Next() {
		var st stuck
		if err := rows.Scan(&st.runID, &st.taskID); err != nil {
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
		if _, err := s.DB.Exec(
			`UPDATE tasks SET verify_verdict='inconclusive',
			                  verify_detail='verification run stalled and was reaped'
			 WHERE id=?`, st.taskID); err != nil {
			log.Printf("error: verify: reap stamp task %d: %v", st.taskID, err)
			continue
		}
		s.notify(st.taskID)
	}
	if len(stucks) > 0 {
		log.Printf("swarmery verify: reaped %d stalled verification run(s)", len(stucks))
	}
	return len(stucks), nil
}

// HealStale reclaims `running` verification_runs rows a crashed/restarted daemon
// left behind: with no live goroutine in THIS process (there can't be — we just
// started), a `running` row is orphaned. Mark it error + stamp the task
// inconclusive so it is re-verifiable (provision/dispatch heal idiom). Unlike
// Reap it ignores age (every running row at startup is by definition orphaned).
func (s *Service) HealStale() error {
	rows, err := s.DB.Query(`SELECT id, task_id FROM verification_runs WHERE status='running'`)
	if err != nil {
		return err
	}
	type stuck struct{ runID, taskID int64 }
	var stucks []stuck
	for rows.Next() {
		var st stuck
		if err := rows.Scan(&st.runID, &st.taskID); err != nil {
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
		s.finishRun(st.runID, "error", "", "interrupted by daemon restart")
		if _, err := s.DB.Exec(
			`UPDATE tasks SET verify_verdict='inconclusive',
			                  verify_detail='verification interrupted by daemon restart'
			 WHERE id=? AND (verify_verdict IS NULL OR verify_verdict='')`, st.taskID); err != nil {
			log.Printf("error: verify: heal stamp task %d: %v", st.taskID, err)
		}
	}
	if len(stucks) > 0 {
		log.Printf("swarmery verify: healed %d interrupted verification run(s)", len(stucks))
	}
	return nil
}
