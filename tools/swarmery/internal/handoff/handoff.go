// Package handoff generates continuation briefs for fat sessions. Marathon
// sessions are the top cost driver (cost ≈ avg_ctx × turns): the cheapest fix
// is ending them earlier, but the user keeps going because restarting means
// reconstructing context by hand. This package removes that friction — when a
// session's context crosses Threshold, the daemon writes
// ~/.swarmery/handoffs/<session_uuid>.md (goal, state, files touched, key
// decisions, next step) from its OWN DB (never the transcript — a 400k-context
// transcript would make the fix as expensive as the disease) via a pinned
// cheap-model headless run, and surfaces it on the session card + detail rail.
// A `## Rejected` section (rejected.go) names the paths the work already ruled
// out, so the fresh session does not walk them again.
package handoff

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cachecold"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const (
	// Threshold is the context footprint (tokens_in + cache_read + cache_write
	// of the newest assistant turn) that makes a live session a handoff
	// candidate. Matches the fat-session cost driver: at this occupancy every
	// continuation re-reads a near-full window.
	Threshold = 150_000
	// RegrowthDelta gates regeneration: after a handoff, the session must grow
	// its context by at least this much past the last brief's footprint before
	// a fresh brief is worth another paid run. Without it a still-open fat
	// session would regenerate every tick.
	RegrowthDelta = 75_000
	// ActivityWin bounds "still alive": only sessions whose newest turn ended
	// within this window are candidates. A session no one is touching needs no
	// handoff.
	ActivityWin = 2 * time.Hour
	// MaxPerTick caps generations per tick — each is a paid model run. Overflow
	// is counted and logged by the caller, never silently dropped.
	MaxPerTick = 3
)

const (
	// assistantTextCap / userTextCap bound how much of each turn's prose lands
	// in the digest — the generator needs signal, not the whole transcript.
	assistantTextCap = 1_500
	userTextCap      = 800
	// maxAssistantTurns / maxUserTurns: how many recent non-empty turns to
	// include (assistant carries the work, user carries the intent).
	maxAssistantTurns = 15
	maxUserTurns      = 5
	// maxFiles caps the touched-files section.
	maxFiles = 40
)

// Candidate is one fat, live session eligible for a handoff brief.
type Candidate struct {
	SessionID     int64
	SessionUUID   string
	ContextTokens int64
}

// Runner executes one generation prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// Candidates returns the fat, live, non-System sessions eligible for a fresh
// handoff, ordered by context footprint DESC and capped at MaxPerTick. The
// second return is the overflow count (candidates dropped by the cap) so the
// caller can log it — no silent caps.
func Candidates(db *sql.DB, now time.Time) ([]Candidate, int, error) {
	return candidatesWithSysDir(db, now, systemDir())
}

// systemDir is the System project's cwd the candidate rules exclude.
func systemDir() string {
	if d := ingest.SystemDir(); d != "" {
		return d
	}
	// A sentinel that no real cwd equals, so the exclusion is a no-op rather
	// than accidentally matching "" and excluding everything.
	return "\x00unresolvable"
}

// candidatesWithSysDir is the testable core of Candidates with the System dir
// injected (production derives it from ingest.SystemDir()).
func candidatesWithSysDir(db *sql.DB, now time.Time, sysDir string) ([]Candidate, int, error) {
	all, err := queryCandidates(db, now.Add(-ActivityWin).UTC().Format(time.RFC3339), sysDir, 0)
	if err != nil {
		return nil, 0, err
	}
	dropped := 0
	if len(all) > MaxPerTick {
		dropped = len(all) - MaxPerTick
		all = all[:MaxPerTick]
	}
	return all, dropped, nil
}

// Eligible reports whether one session passes the candidate rules — the very
// query Candidates runs, filtered to that session and WITHOUT the activity
// window. It gates an explicit request (the «continue from handoff» action of
// a cache-cold session, internal/cachecold): such a session is idle by
// definition, and the operator asking now is the activity. Everything else —
// the threshold, the System/archived/hidden/pruned exclusions, the regrowth
// cooldown — holds exactly as on the tick.
func Eligible(db *sql.DB, sessionID int64) (bool, error) {
	return eligibleWithSysDir(db, sessionID, systemDir())
}

func eligibleWithSysDir(db *sql.DB, sessionID int64, sysDir string) (bool, error) {
	// "" is below every stored timestamp, so the activity predicate holds for
	// any turn that has an ended_at.
	all, err := queryCandidates(db, "", sysDir, sessionID)
	return len(all) == 1, err
}

// Footprint is the context footprint Threshold is compared against —
// tokens_in + cache_read + cache_write of one assistant turn, the measure the
// candidate query computes in SQL. The formula itself lives in internal/cachecold
// (a leaf the ingest tail can import; this package imports ingest), so the
// handoff threshold and the cache-cold prediction can never measure two
// different things.
func Footprint(t cachecold.Turn) int64 { return cachecold.Footprint(t) }

// queryCandidates runs the candidate selection: activity cutoff as an RFC3339
// lower bound on the newest turn's end, sessionID 0 for every session.
func queryCandidates(db *sql.DB, cutoff, sysDir string, sessionID int64) ([]Candidate, error) {
	// ctx: newest assistant turn's footprint per session (same window pass and
	// formula as sessionDTO.ContextTokens / advisor.r9FatSessions). last: that
	// turn's ended_at, for the activity window. ho: latest handoff footprint,
	// for the regrowth cooldown.
	rows, err := db.Query(`
		SELECT s.id, s.session_uuid, ctx.context_tokens
		FROM sessions s
		JOIN projects p ON p.id = s.project_id
		JOIN (
			SELECT session_id,
			       COALESCE(tokens_in,0)+COALESCE(tokens_cache_read,0)+COALESCE(tokens_cache_write,0) AS context_tokens,
			       ended_at,
			       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq DESC) AS rn
			FROM turns
			WHERE role = 'assistant'
			  AND (tokens_in IS NOT NULL OR tokens_cache_read IS NOT NULL OR tokens_cache_write IS NOT NULL)
		) ctx ON ctx.session_id = s.id AND ctx.rn = 1
		LEFT JOIN (
			SELECT session_id, context_tokens,
			       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY created_at DESC, id DESC) AS rn
			FROM handoffs
		) ho ON ho.session_id = s.id AND ho.rn = 1
		WHERE p.archived = 0
		  AND s.hidden = 0
		  AND s.pruned = 0
		  AND COALESCE(s.cwd, '') NOT IN (?, '/')
		  AND ctx.context_tokens >= ?
		  AND ctx.ended_at IS NOT NULL
		  AND ctx.ended_at >= ?
		  AND (ho.context_tokens IS NULL OR ho.context_tokens + ? <= ctx.context_tokens)
		  AND (? = 0 OR s.id = ?)
		ORDER BY ctx.context_tokens DESC, s.id`,
		sysDir, Threshold, cutoff, RegrowthDelta, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.SessionID, &c.SessionUUID, &c.ContextTokens); err != nil {
			return nil, err
		}
		all = append(all, c)
	}
	return all, rows.Err()
}

// Digest builds the generator input from the DB (never the transcript). Layout:
// a session header, the repository anchor, the touched-files table, the
// structured rejected paths, then the recent user/assistant prose (truncated,
// empties skipped).
func Digest(db *sql.DB, sessionID int64) (string, error) {
	d, err := buildDigest(db, sessionID)
	return d.text, err
}

// digest is the generator input plus the Go-recorded facts it embedded, so
// generateInto writes the very same facts the model saw — computed once.
type digest struct {
	text     string
	anc      anchor
	rejected []Rejected
}

func buildDigest(db *sql.DB, sessionID int64) (digest, error) {
	var b strings.Builder

	// Session header.
	var uuid string
	var title, cwd, branch, started sql.NullString
	err := db.QueryRow(`
		SELECT session_uuid, COALESCE(custom_title, title, ''), COALESCE(cwd, ''), COALESCE(git_branch, ''), started_at
		FROM sessions WHERE id = ?`, sessionID).Scan(&uuid, &title, &cwd, &branch, &started)
	if err != nil {
		return digest{}, err
	}
	fmt.Fprintf(&b, "## Session\n")
	fmt.Fprintf(&b, "- Title: %s\n", nz(title.String, "unknown"))
	fmt.Fprintf(&b, "- Working dir: %s\n", nz(cwd.String, "unknown"))
	fmt.Fprintf(&b, "- Git branch: %s\n", nz(branch.String, "unknown"))
	fmt.Fprintf(&b, "- Started: %s\n\n", nz(started.String, "unknown"))

	// Repository anchor: recorded by Go from the repo itself, not the DB.
	anc := repoAnchor(cwd.String)
	if anc.Known && anc.Branch == "" {
		anc.Branch = strings.TrimSpace(branch.String)
	}
	b.WriteString(anc.Markdown())
	b.WriteString("\n")

	// Touched files, aggregated per path, most-changed first.
	fcRows, err := db.Query(`
		SELECT file_path,
		       GROUP_CONCAT(DISTINCT change_type),
		       COALESCE(SUM(additions), 0),
		       COALESCE(SUM(deletions), 0)
		FROM file_changes
		WHERE session_id = ?
		GROUP BY file_path
		ORDER BY (COALESCE(SUM(additions),0) + COALESCE(SUM(deletions),0)) DESC
		LIMIT ?`, sessionID, maxFiles)
	if err != nil {
		return digest{}, err
	}
	b.WriteString("## Files touched\n")
	anyFile := false
	for fcRows.Next() {
		var path, kinds string
		var adds, dels int64
		if err := fcRows.Scan(&path, &kinds, &adds, &dels); err != nil {
			fcRows.Close()
			return digest{}, err
		}
		fmt.Fprintf(&b, "- %s (%s, +%d/-%d)\n", path, kinds, adds, dels)
		anyFile = true
	}
	fcRows.Close()
	if err := fcRows.Err(); err != nil {
		return digest{}, err
	}
	if !anyFile {
		b.WriteString("- (none recorded)\n")
	}
	b.WriteString("\n")

	// Rejected paths recorded in the DB (rejected.go). An optional input: a
	// failed read is logged and the brief goes on with the turn text alone.
	rejected, err := StructuredRejected(db, uuid)
	if err != nil {
		log.Printf("warning: handoff: session %d: structured rejected: %v", sessionID, err)
		rejected = nil
	}
	b.WriteString(rejectedDigestHeading + "\n")
	if len(rejected) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, r := range rejected {
		b.WriteString(r.Line() + "\n")
	}
	b.WriteString("\n")

	// Recent user prose (intent), oldest→newest for readability.
	userTexts, err := recentTurns(db, sessionID, "user", maxUserTurns, userTextCap)
	if err != nil {
		return digest{}, err
	}
	b.WriteString("## Recent user messages\n")
	if len(userTexts) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, t := range userTexts {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	b.WriteString("\n")

	// Recent assistant prose (the work), oldest→newest.
	asstTexts, err := recentTurns(db, sessionID, "assistant", maxAssistantTurns, assistantTextCap)
	if err != nil {
		return digest{}, err
	}
	b.WriteString("## Recent assistant messages\n")
	if len(asstTexts) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, t := range asstTexts {
		fmt.Fprintf(&b, "- %s\n", t)
	}

	return digest{text: b.String(), anc: anc, rejected: rejected}, nil
}

// recentTurns returns up to `limit` non-empty turns of the given role, each
// truncated to `cap` chars, in ascending seq order. The newest `limit` turns
// are selected (a subquery), then re-sorted oldest→newest.
func recentTurns(db *sql.DB, sessionID int64, role string, limit, cap int) ([]string, error) {
	rows, err := db.Query(`
		SELECT text FROM (
			SELECT seq, text FROM turns
			WHERE session_id = ? AND role = ? AND text IS NOT NULL AND TRIM(text) != ''
			ORDER BY seq DESC
			LIMIT ?
		) ORDER BY seq ASC`, sessionID, role, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		out = append(out, truncate(text, cap))
	}
	return out, rows.Err()
}

// Generate builds the digest, runs the model, writes the brief to
// ~/.swarmery/handoffs/<session_uuid>.md (0644), records a handoffs row with
// the current context footprint, and returns the path.
func Generate(db *sql.DB, r Runner, sessionID int64, now time.Time) (string, error) {
	dir, err := HandoffsDir()
	if err != nil {
		return "", err
	}
	return generateInto(db, r, sessionID, now, dir)
}

// generateInto is the testable core of Generate with the output directory
// injected (production uses HandoffsDir()).
func generateInto(db *sql.DB, r Runner, sessionID int64, now time.Time, dir string) (string, error) {
	// Session uuid + current footprint (stored on the row for the cooldown).
	var uuid string
	var ctxTokens int64
	err := db.QueryRow(`
		SELECT s.session_uuid, COALESCE(ctx.context_tokens, 0)
		FROM sessions s
		LEFT JOIN (
			SELECT session_id,
			       COALESCE(tokens_in,0)+COALESCE(tokens_cache_read,0)+COALESCE(tokens_cache_write,0) AS context_tokens,
			       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY seq DESC) AS rn
			FROM turns
			WHERE role = 'assistant'
			  AND (tokens_in IS NOT NULL OR tokens_cache_read IS NOT NULL OR tokens_cache_write IS NOT NULL)
		) ctx ON ctx.session_id = s.id AND ctx.rn = 1
		WHERE s.id = ?`, sessionID).Scan(&uuid, &ctxTokens)
	if err != nil {
		return "", err
	}

	dig, err := buildDigest(db, sessionID)
	if err != nil {
		return "", err
	}
	prompt := promptTemplate + dig.text

	out, err := r.Run(context.Background(), prompt)
	if err != nil {
		return "", err
	}
	// The Rejected section is held to what the digest promised (present, every
	// structured line verbatim, capped), and the anchor is appended by Go, never
	// by the model: the next session can check these facts against the repo
	// instead of trusting a retelling.
	body := strings.TrimRight(fitRejected(out, dig.rejected), "\n") + "\n\n" + dig.anc.Markdown()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, uuid+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}

	if _, err := db.Exec(`
		INSERT INTO handoffs (session_id, path, context_tokens, created_at)
		VALUES (?, ?, ?, ?)`,
		sessionID, path, ctxTokens, now.UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	return path, nil
}

// HandoffsDir is ~/.swarmery/handoffs — the daemon owns ~/.swarmery, so in
// production it is always resolvable.
func HandoffsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".swarmery", "handoffs"), nil
}

// promptTemplate is the verbatim generator instruction; the digest is appended.
const promptTemplate = `You are writing a session HANDOFF file. A developer will /clear their overloaded
session and start a fresh one that begins by reading this file. Write ONLY the
markdown file content, no preamble. Required sections, in order:
# Handoff: <one-line goal>
## State — what is done and verified vs in progress (bullet list)
## Files touched — from the list below, with one-phrase why per file
## Key decisions — decisions/constraints the fresh session must not re-litigate
## Non-goals — what this session deliberately did not do or ruled out
## Rejected — options already ruled out, one "- <option> — <why>" line each, ≤10 lines: copy every
   line of "Rejected (structured)" below verbatim, then add options the messages below show were
   tried and abandoned or ruled out; write "- none recorded" when there are none
## Open risks & failed checks — anything unverified, failing, or flaky, with the command that shows it
## Next step — the single concrete next action, with the exact command or file to start from
## Verification — commands that prove the work still passes
Hard limits: ≤120 lines total. No invented facts: if the digest below doesn't say
it, don't claim it — write "unknown" instead.
Do not restate the repository anchor; it is appended automatically.

=== SESSION DIGEST ===
`

// truncate returns the first n runes of s, appending an ellipsis when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// nz returns fallback when s is empty.
func nz(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
