// GET /api/needs-you — the one list of everything currently blocked on the
// operator, oldest blocker first (needs-you queue, phase 3).
//
// Read-only. Four sources, each one query, each honouring ?project= through
// scopeFilter (scope.go). Archived projects are deliberately NOT filtered —
// same invariant as listApprovals: a live agent blocked in an archived project
// must never be invisible.
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Item kinds, in tie-break order (needsYouKindRank).
const (
	needsYouApproval        = "approval"
	needsYouQuestion        = "question"
	needsYouProdDeployLocal = "prod_deploy_local"
	needsYouAwaitingReply   = "awaiting_reply"
	needsYouFailed          = "failed"
)

// needsYouKindRank orders items that block since the same instant.
var needsYouKindRank = map[string]int{
	needsYouApproval:        0,
	needsYouQuestion:        1,
	needsYouProdDeployLocal: 2,
	needsYouAwaitingReply:   3,
	needsYouFailed:          4,
}

const (
	// needsYouWindow bounds the time-limited sources (prod_deploy_local, failed).
	needsYouWindow = 24 * time.Hour
	// needsYouPreviewMax caps preview (runes, ellipsis included).
	needsYouPreviewMax = 200
	// needsYouQuestionMax caps question (runes, ellipsis included).
	needsYouQuestionMax = 600
	// needsYouTSFormat is the store's timestamp layout (approvals.tsFormat).
	needsYouTSFormat = "2006-01-02T15:04:05.000Z"
	// askUserQuestionTool is the tool whose pending request is a question.
	askUserQuestionTool = "AskUserQuestion"
)

// replySuggestion is the Haiku-extracted reply card (internal/replyextract):
// the reply_extracts 'ok' row for the session's newest main-thread assistant
// turn; null when the extractor is off or has nothing for that turn.
type replySuggestion struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended"`
}

// needsYouItem is one blocker. Mirrors NeedsYouItem in web/src/api/types.ts.
type needsYouItem struct {
	Kind        string `json:"kind"` // approval | question | prod_deploy_local | awaiting_reply | failed
	SessionID   int64  `json:"sessionId"`
	SessionUUID string `json:"sessionUuid"`
	// SessionName is COALESCE(custom_title, title, session_uuid[:8]).
	SessionName string `json:"sessionName"`
	ProjectSlug string `json:"projectSlug"`
	// RequestID is the permission_requests.id (approval/question/prod_deploy_local).
	RequestID *int64 `json:"requestId"`
	ToolName  string `json:"toolName"`
	// Preview is ≤200 chars: the tool argument (approvals.ArgOf), the
	// question text, or the head of an awaiting_reply question.
	Preview string `json:"preview"`
	// Question (awaiting_reply): the last paragraph of the newest main-thread
	// assistant prose, ≤600 chars.
	Question string `json:"question"`
	// AsksQuestion: that paragraph carries a '?' — a display hint only.
	AsksQuestion    bool             `json:"asksQuestion"`
	BlockingSince   string           `json:"blockingSince"`
	BlockingSeconds int64            `json:"blockingSeconds"`
	TermFocusURL    *string          `json:"termFocusUrl"`
	Suggestion      *replySuggestion `json:"suggestion"`

	at time.Time // parsed BlockingSince; zero when unparseable
}

// needsYouResponse mirrors NeedsYouResponse in web/src/api/types.ts.
type needsYouResponse struct {
	Items       []needsYouItem `json:"items"`
	GeneratedAt string         `json:"generatedAt"`
}

// needsYouSessionCols is the session projection every source shares; it
// expects sessions aliased s and projects aliased p.
const needsYouSessionCols = `
	s.id, s.session_uuid,
	COALESCE(NULLIF(s.custom_title, ''), NULLIF(s.title, ''), substr(s.session_uuid, 1, 8)),
	p.slug, s.term_focus_url`

func (h *Handler) needsYou(w http.ResponseWriter, r *http.Request) {
	items, err := collectNeedsYou(h.DB, r, time.Now().UTC())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, needsYouResponse{Items: items, GeneratedAt: time.Now().UTC().Format(needsYouTSFormat)}, nil)
}

// collectNeedsYou runs the four source queries (sequentially — the store is a
// single connection), dedupes and sorts. now anchors the 24 h windows and
// blockingSeconds.
func collectNeedsYou(db *sql.DB, r *http.Request, now time.Time) ([]needsYouItem, error) {
	scope, projArgs := scopeFilter(r)
	cutoff := now.Add(-needsYouWindow).Format(needsYouTSFormat)

	pending, err := queryNeedsYouRequests(db, `pr.status = 'pending'`, nil, scope, projArgs)
	if err != nil {
		return nil, err
	}
	// A prod deploy handed to the terminal blocks until the transcript moves
	// past it (requested_at >= ended_at: no activity since the hand-off).
	// The status term lets SQLite use idx_pr_pending(status, requested_at).
	local, err := queryNeedsYouRequests(db,
		`pr.status = ? AND pr.risk_class = ? AND pr.resolved_via = ?
		 AND pr.requested_at >= COALESCE(s.ended_at, '') AND pr.requested_at >= ?`,
		[]any{approvals.StatusResolvedElsewhere, approvals.RiskProdDeploy, approvals.ViaLocalOnly, cutoff}, scope, projArgs)
	if err != nil {
		return nil, err
	}
	awaiting, err := queryNeedsYouSessions(db, needsYouAwaitingReply,
		`s.status = ?`, []any{ingest.StatusAwaitingReply}, scope, projArgs)
	if err != nil {
		return nil, err
	}
	// Mirrors the notch's erroredSessions (AttentionModel.swift): a live status
	// over a dead process, or an operator verdict of fail.
	failed, err := queryNeedsYouSessions(db, needsYouFailed,
		`s.ended_at >= ? AND (s.outcome = 'fail'
		   OR (s.status IN ('active', 'idle', ?) AND s.proc_state = 'dead'))`,
		[]any{cutoff, ingest.StatusAwaitingReply}, scope, projArgs)
	if err != nil {
		return nil, err
	}

	all := make([]needsYouItem, 0, len(pending)+len(local)+len(awaiting)+len(failed))
	all = append(all, pending...)
	all = append(all, local...)
	all = append(all, awaiting...)
	all = append(all, failed...)
	items := dedupeNeedsYou(all)
	for i := range items {
		items[i].BlockingSeconds = blockingSeconds(items[i].at, now)
	}
	sortNeedsYou(items)
	return items, nil
}

// queryNeedsYouRequests reads permission_requests rows matching where:
// pending ones become approval/question, local-only prod deploys
// prod_deploy_local.
func queryNeedsYouRequests(db *sql.DB, where string, args []any, scope string, projArgs []any) ([]needsYouItem, error) {
	q := `SELECT` + needsYouSessionCols + `, pr.id, pr.tool_name, pr.request_json, pr.requested_at, pr.status
		FROM permission_requests pr
		JOIN sessions s ON s.id = pr.session_id
		JOIN projects p ON p.id = s.project_id
		WHERE ` + where + scope
	rows, err := db.Query(q, append(append([]any{}, args...), projArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var it needsYouItem
		var reqID int64
		var requestJSON, status string
		if err := rows.Scan(&it.SessionID, &it.SessionUUID, &it.SessionName, &it.ProjectSlug, &it.TermFocusURL,
			&reqID, &it.ToolName, &requestJSON, &it.BlockingSince, &status); err != nil {
			return nil, err
		}
		it.RequestID = &reqID
		it.Kind = requestKind(status, it.ToolName)
		it.Preview = requestPreview(it.ToolName, requestJSON)
		it.at = parseNeedsYouTS(it.BlockingSince)
		out = append(out, it)
	}
	return out, rows.Err()
}

// queryNeedsYouSessions reads sessions matching where as items of kind.
// Soft-hidden sessions are skipped: the operator dismissed them. Only an
// awaiting_reply item carries the newest main-thread assistant prose and,
// when the extractor stored one for that same turn, its reply suggestion.
func queryNeedsYouSessions(db *sql.DB, kind, where string, args []any, scope string, projArgs []any) ([]needsYouItem, error) {
	turnCols, turnJoins := `NULL, NULL, NULL, NULL`, ``
	if kind == needsYouAwaitingReply {
		// The turn choice must match replyextract.latestTurn: that turn id is
		// the reply_extracts cache key.
		turnCols = `lt.text, rx.question, rx.options_json, rx.recommended`
		turnJoins = `
		LEFT JOIN turns lt ON lt.id = (SELECT t.id FROM turns t
		             WHERE t.session_id = s.id AND t.agent_name IS NULL AND t.role = 'assistant'
		               AND TRIM(COALESCE(t.text, '')) != ''
		             ORDER BY t.seq DESC LIMIT 1)
		LEFT JOIN reply_extracts rx ON rx.session_id = s.id AND rx.turn_id = lt.id AND rx.status = 'ok'`
	}
	q := `SELECT` + needsYouSessionCols + `, COALESCE(s.ended_at, s.started_at), ` + turnCols + `
		FROM sessions s
		JOIN projects p ON p.id = s.project_id` + turnJoins + `
		WHERE s.hidden = 0 AND ` + where + scope
	rows, err := db.Query(q, append(append([]any{}, args...), projArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var it needsYouItem
		var text, sugQuestion, sugOptions, sugRecommended sql.NullString
		if err := rows.Scan(&it.SessionID, &it.SessionUUID, &it.SessionName, &it.ProjectSlug, &it.TermFocusURL,
			&it.BlockingSince, &text, &sugQuestion, &sugOptions, &sugRecommended); err != nil {
			return nil, err
		}
		it.Kind = kind
		if para := lastParagraph(text.String); para != "" {
			it.AsksQuestion = strings.ContainsAny(para, "?？")
			it.Question = clipTail(para, needsYouQuestionMax)
			it.Preview = clipHead(para, needsYouPreviewMax)
		}
		it.Suggestion = suggestionOf(sugQuestion, sugOptions, sugRecommended)
		it.at = parseNeedsYouTS(it.BlockingSince)
		out = append(out, it)
	}
	return out, rows.Err()
}

// suggestionOf builds the reply card from a reply_extracts 'ok' row; nil when
// there is none. Unreadable options degrade to [] rather than hiding the card.
func suggestionOf(question, optionsJSON, recommended sql.NullString) *replySuggestion {
	if !question.Valid || question.String == "" {
		return nil
	}
	s := &replySuggestion{Question: question.String, Options: []string{}, Recommended: recommended.String}
	if optionsJSON.Valid {
		var opts []string
		if json.Unmarshal([]byte(optionsJSON.String), &opts) == nil && opts != nil {
			s.Options = opts
		}
	}
	return s
}

// requestKind maps a permission_requests row onto its item kind: a pending
// AskUserQuestion is a question, any other pending row an approval, and a
// resolved row (only the local-only prod deploy is ever selected) a
// prod_deploy_local.
func requestKind(status, toolName string) string {
	switch {
	case status != approvals.StatusPending:
		return needsYouProdDeployLocal
	case toolName == askUserQuestionTool:
		return needsYouQuestion
	default:
		return needsYouApproval
	}
}

// requestPreview is the ≤200-char one-liner for a request: the first question
// of an AskUserQuestion (with a "+N more" tail), else the tool argument.
// Empty when request_json carries neither.
func requestPreview(toolName, requestJSON string) string {
	var p struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &p); err != nil {
		return ""
	}
	if toolName == askUserQuestionTool {
		var in struct {
			Questions []struct {
				Question string `json:"question"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || len(in.Questions) == 0 {
			return ""
		}
		q := strings.TrimSpace(in.Questions[0].Question)
		if extra := len(in.Questions) - 1; extra > 0 {
			q += " (+" + strconv.Itoa(extra) + " more)"
		}
		return clipHead(q, needsYouPreviewMax)
	}
	arg, ok := approvals.ArgOf(toolName, p.ToolInput)
	if !ok {
		return ""
	}
	return clipHead(arg, needsYouPreviewMax)
}

// lastParagraph returns the text after the final blank line (a line that is
// empty once trimmed), trimmed. Trailing blank lines are ignored; text with no
// blank line is returned whole.
func lastParagraph(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		start--
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

// clipHead caps s at max runes, keeping the head and ending in "…".
func clipHead(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// clipTail caps s at max runes, keeping the tail (where a question sits) and
// starting with "…".
func clipTail(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return "…" + strings.TrimSpace(string(runes[len(runes)-(max-1):]))
}

// dedupeNeedsYou drops an awaiting_reply item whose session is also listed as
// failed: one session, one item, the failure wins.
func dedupeNeedsYou(items []needsYouItem) []needsYouItem {
	failedSessions := map[int64]bool{}
	for _, it := range items {
		if it.Kind == needsYouFailed {
			failedSessions[it.SessionID] = true
		}
	}
	out := make([]needsYouItem, 0, len(items))
	for _, it := range items {
		if it.Kind == needsYouAwaitingReply && failedSessions[it.SessionID] {
			continue
		}
		out = append(out, it)
	}
	return out
}

// sortNeedsYou orders items oldest blocker first; ties break on kind
// (approval < question < prod_deploy_local < awaiting_reply < failed), then
// on the item id (request id, else session id).
func sortNeedsYou(items []needsYouItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		if ra, rb := needsYouKindRank[a.Kind], needsYouKindRank[b.Kind]; ra != rb {
			return ra < rb
		}
		return needsYouItemID(a) < needsYouItemID(b)
	})
}

// needsYouItemID is the tie-break id: the request id when there is one.
func needsYouItemID(it needsYouItem) int64 {
	if it.RequestID != nil {
		return *it.RequestID
	}
	return it.SessionID
}

// blockingSeconds is how long the item has blocked, never negative; 0 when
// its timestamp did not parse.
func blockingSeconds(at, now time.Time) int64 {
	if at.IsZero() || now.Before(at) {
		return 0
	}
	return int64(now.Sub(at) / time.Second)
}

// parseNeedsYouTS parses a stored RFC 3339 timestamp; zero when it does not.
func parseNeedsYouTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
