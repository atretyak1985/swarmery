package store

// Board cards: the ONE constructor every board-card writer goes through.
//
// Five call sites used to hand-write the same row (the board POST, the routine
// create-task step, the verifier's fix task, capture, and the LLM extractor),
// and each drifted a little: one skipped the title/prompt check, one wrote an
// origin the validation set did not know, none could carry the provenance
// columns 0066 added. A card that reaches the board with an empty title or
// prompt renders as a blank tile with nothing to act on, so the guard belongs
// where the row is minted and nowhere else. internal/store is the bottom of
// the import graph — every writer can reach it — which is why the constructor
// lives here rather than in the api or taskcap packages.
//
// The one writer that is NOT a board card and stays outside: internal/wsingest
// projects on-disk workspace plan cards into the same table with
// source='workspace' and an upsert keyed on (workspace_id, external_id); those
// rows are owned by the disk, not by the board, and the sweeper/dispatcher
// never touch them. store/insert_sites_test.go pins that exception by name.

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DB is the storage surface a board insert needs. Both *sql.DB and *sql.Tx
// satisfy it: the API layer writes against the pool while ingest captures
// inside the transaction that is already writing the transcript's turns, so a
// card can never outlive a rolled-back tail batch.
type DB interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

const (
	// BoardTSFormat is the millisecond-Z stamp every board timestamp carries
	// (created_at, column_moved_at, archived_at). The board sorts and compares
	// these LEXICALLY, so every writer must agree on one shape.
	BoardTSFormat = "2006-01-02T15:04:05.000Z"

	// NormalPriority is the default on the INTEGER priority scale (0001_init):
	// urgent=1 < high=3 < normal=5 < low=7.
	NormalPriority = 5

	// externalIDAlphabet / externalIDLen mint the "T-xxxxxx" card id the
	// dispatcher and commit trailers reference.
	externalIDAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	externalIDLen      = 6
)

// boardColumns is the closed set of kanban columns. Validated in Go; the
// migration defaults existing rows to triage.
var boardColumns = map[string]bool{
	"triage":      true,
	"todo":        true,
	"in_progress": true,
	"in_review":   true,
	"done":        true,
	"archived":    true,
}

// ValidColumn reports whether c is in the closed board-column set. Pure.
func ValidColumn(c string) bool { return boardColumns[c] }

// origins is the closed set of task provenances (0048 + the verifier's
// fix-chain marker): 'manual' for a hand-written card, 'session' / 'llm' for
// one captured from a session, 'verify-fix' for a fix task the verifier minted
// off a failed verdict, 'github' for an inbox card the GitHub inbox routine
// keeps in step with one issue or pull request (internal/ghinbox). One map,
// read by every validator, so the HTTP check, the capture path and the
// constructor can never disagree about the set.
var origins = map[string]bool{
	"manual":     true,
	"session":    true,
	"llm":        true,
	"verify-fix": true,
	"github":     true,
}

// ValidOrigin reports whether o is in the closed provenance set. Pure.
func ValidOrigin(o string) bool { return origins[o] }

// NewExternalID mints a "T-" + 6-char base36 card id. The tasks.id INTEGER PK
// is autoincremented by SQLite; this string is the external_id.
func NewExternalID() (string, error) {
	buf := make([]byte, externalIDLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = externalIDAlphabet[int(buf[i])%len(externalIDAlphabet)]
	}
	return "T-" + string(buf), nil
}

// BoardTaskInput is everything a board card varies by. Zero values take the
// board's defaults: Priority 0 ⇒ normal, Column "" ⇒ triage, Origin "" ⇒
// manual, ExternalID "" ⇒ minted, Now zero ⇒ time.Now(). Fixed by construction
// and not settable: source='queue', status='queued'.
type BoardTaskInput struct {
	ProjectID int64
	Title     string
	Prompt    string
	Priority  int
	Column    string
	// Origin is who minted the card (see origins). OriginSessionID,
	// OriginTurnUUID, OriginQuote and OriginFiles are the capture provenance
	// (0066) and are meaningful only for a non-manual origin.
	Origin          string
	OriginSessionID *int64
	OriginTurnUUID  string
	OriginQuote     string
	OriginFiles     []string
	// CaptureKey is the capture idempotency key (0048). When set, the insert
	// is an upsert-or-nothing on the partial unique index and a replay reports
	// inserted=false with the existing row's id.
	CaptureKey string
	// ExternalID overrides the minted "T-xxxxxx" id. The verifier uses it to
	// make a fix task carry its ROOT's id so the fix's own failure charges the
	// root's budget.
	ExternalID string
	Model      *string
	Playbook   *string
	Agent      *string
	// FileScope / Labels / Dependencies are stored as JSON arrays; nil ⇒ [].
	FileScope    []string
	Labels       []string
	Dependencies []string
	// Now is the creation instant (test/clock seam); zero ⇒ wall clock.
	Now time.Time
}

// InsertBoardTask mints one board card and reports (id, inserted).
//
// inserted is false only for a CaptureKey replay: the row already existed and
// its id is returned so the caller can still link to it. Every other outcome
// is either a fresh row (true) or an error. Validation failures are errors —
// this is the internal write path, and each caller maps them to its own
// surface (HTTP 400, a routine step failure, a verifier log line).
func InsertBoardTask(db DB, in BoardTaskInput) (int64, bool, error) {
	title := strings.TrimSpace(in.Title)
	prompt := strings.TrimSpace(in.Prompt)
	if title == "" || prompt == "" {
		return 0, false, errors.New("board task: title and prompt are required")
	}
	column := in.Column
	if column == "" {
		column = "triage"
	}
	if !ValidColumn(column) {
		return 0, false, fmt.Errorf("board task: unknown column %q", in.Column)
	}
	origin := in.Origin
	if origin == "" {
		origin = "manual"
	}
	if !ValidOrigin(origin) {
		return 0, false, fmt.Errorf("board task: invalid origin %q", in.Origin)
	}
	priority := in.Priority
	if priority == 0 {
		priority = NormalPriority
	}
	extID := strings.TrimSpace(in.ExternalID)
	if extID == "" {
		minted, err := NewExternalID()
		if err != nil {
			return 0, false, err
		}
		extID = minted
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().Format(BoardTSFormat)
	// An explicit non-default landing column counts as a move; a card born in
	// triage has never moved, and the inbox sweeper's idle clock relies on
	// that NULL (taskcap.InboxIdleSince).
	var movedAt any
	if column != "triage" {
		movedAt = stamp
	}
	scope, err := marshalList(in.FileScope)
	if err != nil {
		return 0, false, err
	}
	labels, err := marshalList(in.Labels)
	if err != nil {
		return 0, false, err
	}
	deps, err := marshalList(in.Dependencies)
	if err != nil {
		return 0, false, err
	}
	var files any
	if len(in.OriginFiles) > 0 {
		s, err := marshalList(in.OriginFiles)
		if err != nil {
			return 0, false, err
		}
		files = s
	}
	key := strings.TrimSpace(in.CaptureKey)

	q := `
		INSERT INTO tasks (project_id, title, prompt, priority, status, created_at,
		                   source, external_id, board_column, model, playbook, agent,
		                   file_scope, labels, dependencies, column_moved_at,
		                   origin, origin_session_id, capture_key,
		                   origin_turn_uuid, origin_quote, origin_files)
		VALUES (?, ?, ?, ?, 'queued', ?, 'queue', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if key != "" {
		// The partial index's predicate must be repeated in the conflict target
		// — SQLite needs it to know which index the upsert is aimed at.
		q += ` ON CONFLICT(capture_key) WHERE capture_key IS NOT NULL DO NOTHING`
	}
	res, err := db.Exec(q,
		in.ProjectID, title, prompt, priority, stamp,
		extID, column, nullable(in.Model), nullable(in.Playbook), nullable(in.Agent),
		scope, labels, deps, movedAt,
		origin, in.OriginSessionID, nullableStr(key),
		nullableStr(strings.TrimSpace(in.OriginTurnUUID)),
		nullableStr(strings.TrimSpace(in.OriginQuote)), files)
	if err != nil {
		return 0, false, fmt.Errorf("board task: insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		// Only reachable on a capture-key replay: hand back the existing id.
		var id int64
		if err := db.QueryRow(`SELECT id FROM tasks WHERE capture_key = ?`, key).Scan(&id); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// marshalList renders xs as a compact JSON array; nil/empty ⇒ "[]".
func marshalList(xs []string) (string, error) {
	if len(xs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// nullable maps a nil or blank *string to SQL NULL.
func nullable(p *string) any {
	if p == nil {
		return nil
	}
	return nullableStr(strings.TrimSpace(*p))
}

// nullableStr maps "" to SQL NULL.
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
