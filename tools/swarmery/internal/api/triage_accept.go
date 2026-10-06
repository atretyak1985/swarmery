package api

// Inbox triage agent (phase 4): the operator accepts a triage suggestion — a
// `suggested` verdict on an item the run could not decide by itself — and the
// daemon performs the action the verdict names. These two endpoints are the
// only path by which triage accepts a lesson, confirms a retirement, accepts a
// recommendation, creates a board card or starts improve.
//
// The accept is CLAIM-FIRST: everything the action needs is validated, then
// the verdict is moved suggested → accepted (a guarded UPDATE only one caller
// wins), and only then is the action performed. A failed action hands the
// suggestion back (accepted → suggested); a retry can never act twice.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

const (
	cardTitleMaxRunes  = 80
	cardPromptMaxBytes = 8000

	// accept-all walks suggestions in pages of acceptAllPage and handles at
	// most acceptAllMax per request; the response's "remaining" says what is left.
	acceptAllPage = 200
	acceptAllMax  = 2000
)

var (
	errNotSuggestion = errors.New("verdict is not an open suggestion")
	errItemChanged   = errors.New("the item changed")
)

// acceptAction performs a validated accept; it returns the HTTP status to
// answer with on failure.
type acceptAction func() (int, error)

// POST /api/triage/verdicts/{id}/accept → 200 verdict.
func (h *Handler) acceptTriageVerdict(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid verdict id")
		return
	}
	// The action must finish even if the browser goes away mid-request.
	v, status, err := h.acceptVerdict(context.WithoutCancel(r.Context()), id)
	switch {
	case err == nil:
		writeJSON(w, v, nil)
	case status == http.StatusConflict && v.State != "":
		writeJSONStatus(w, status, map[string]any{"error": err.Error(), "state": v.State})
	case status >= http.StatusInternalServerError:
		writeErr(w, err)
	default:
		writeClientErr(w, status, err.Error())
	}
}

type acceptAllBody struct {
	Project json.RawMessage `json:"project"`
}

type acceptFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

// POST /api/triage/verdicts/accept-all {"project"?} → 200
// {"accepted":[…],"stale":[…],"failed":[{"id","error"}],"remaining":n}. Every
// suggested verdict in scope (up to acceptAllMax) goes through the same accept
// as the single route, oldest first; a failure never stops the rest. remaining
// counts the suggestions in scope still open when the request returns (failed
// ones included). Samples are never touched.
func (h *Handler) acceptAllTriageVerdicts(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	var body acceptAllBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var projectID int64
	if p := rawScalar(body.Project); p != "" {
		pid, ok, err := h.resolveTriageProject(p)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			writeClientErr(w, http.StatusNotFound, "unknown project")
			return
		}
		projectID = pid
	}

	ctx := context.WithoutCancel(r.Context())
	accepted, stale, failed := []int64{}, []int64{}, []acceptFailure{}
	// Keyset pages, oldest first: a verdict that fails is reopened behind the
	// cursor, so it is never retried within this request.
	var after int64
	for done := 0; done < acceptAllMax; {
		vs, err := triageSvc.ListSuggestedAfter(after, min(acceptAllPage, acceptAllMax-done), projectID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if len(vs) == 0 {
			break
		}
		for _, v := range vs {
			after, done = v.ID, done+1
			_, _, err := h.acceptVerdict(ctx, v.ID)
			switch {
			case err == nil:
				accepted = append(accepted, v.ID)
			case errors.Is(err, errItemChanged):
				stale = append(stale, v.ID)
			default:
				failed = append(failed, acceptFailure{ID: v.ID, Error: err.Error()})
			}
		}
	}
	remaining, err := triageSvc.CountSuggested(projectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"accepted": accepted, "stale": stale, "failed": failed,
		"remaining": remaining}, nil)
}

// acceptVerdict is the per-verdict accept shared by both routes. It returns the
// verdict as it is afterwards, the HTTP status, and the error (nil on success).
func (h *Handler) acceptVerdict(ctx context.Context, id int64) (triage.Verdict, int, error) {
	v, err := triageSvc.GetVerdict(id)
	if errors.Is(err, triage.ErrNotFound) {
		return triage.Verdict{}, http.StatusNotFound, errors.New("verdict not found")
	}
	if err != nil {
		return v, http.StatusInternalServerError, err
	}
	if v.State != triage.StateSuggested {
		return v, http.StatusConflict, errNotSuggestion
	}

	open, err := triageSvc.VerdictOpen(ctx, v)
	if errors.Is(err, triage.ErrNotFound) {
		return triage.Verdict{}, http.StatusConflict, fmt.Errorf("no triage source registered for kind %q", v.Kind)
	}
	if err != nil {
		return v, http.StatusInternalServerError, err
	}
	if !open {
		return markVerdictStale(v)
	}

	act, status, err := h.planAccept(v)
	if err != nil {
		return v, status, err
	}

	// The claim: only one caller moves the verdict out of suggested.
	if _, err := triageSvc.MarkAccepted(id, nil); err != nil {
		if errors.Is(err, triage.ErrNotOpen) {
			cur, _ := triageSvc.GetVerdict(id)
			return cur, http.StatusConflict, errNotSuggestion
		}
		return v, http.StatusInternalServerError, err
	}

	if h.triageAcceptHook != nil {
		h.triageAcceptHook() // test seam: change the item between the open check and the action
	}
	if status, err := act(); err != nil {
		_, rerr := triageSvc.ReopenSuggestion(id)
		if rerr == nil && errors.Is(err, errItemChanged) {
			// The item was decided after the open check: same answer as the pre-check.
			return markVerdictStale(v)
		}
		if rerr != nil {
			err = fmt.Errorf("%w (and the suggestion could not be reopened: %v)", err, rerr)
		}
		cur, _ := triageSvc.GetVerdict(id)
		return cur, status, err
	}
	cur, err := triageSvc.GetVerdict(id)
	if err != nil {
		return cur, http.StatusInternalServerError, err
	}
	return cur, http.StatusOK, nil
}

// markVerdictStale closes suggestion v as stale (its item was decided
// elsewhere) and returns the 409 both stale paths answer with.
func markVerdictStale(v triage.Verdict) (triage.Verdict, int, error) {
	sv, err := triageSvc.MarkStale(v.ID)
	if errors.Is(err, triage.ErrNotOpen) { // closed by someone else meanwhile
		cur, _ := triageSvc.GetVerdict(v.ID)
		return cur, http.StatusConflict, errNotSuggestion
	}
	if err != nil {
		return v, http.StatusInternalServerError, err
	}
	return sv, http.StatusConflict, errItemChanged
}

// planAccept validates everything the action for v needs and returns it
// unperformed; a validation failure is 422 and leaves the verdict untouched.
func (h *Handler) planAccept(v triage.Verdict) (acceptAction, int, error) {
	invalid := func(msg string) (acceptAction, int, error) {
		return nil, http.StatusUnprocessableEntity, errors.New(msg)
	}
	ref, err := strconv.ParseInt(strings.TrimSpace(v.Ref), 10, 64)
	if err != nil || ref <= 0 {
		return invalid("verdict ref " + strconv.Quote(v.Ref) + " is not an id")
	}

	switch v.Kind + "/" + v.Value {
	case "lesson/accept":
		return lessonAction(func(now time.Time) error { _, err := lessons.Accept(h.DB, ref, now); return err }), 0, nil
	case "lesson/not-useful":
		return lessonAction(func(now time.Time) error { _, err := lessons.Dismiss(h.DB, ref, v.Reason, now); return err }), 0, nil
	case "retire/stop":
		return lessonAction(func(now time.Time) error {
			_, err := lessons.ConfirmRetirement(h.DB, lessonVerifyCfg, ref, now)
			return err
		}), 0, nil
	case "retire/keep":
		return lessonAction(func(now time.Time) error {
			_, err := lessons.KeepLesson(h.DB, lessonVerifyCfg, ref, now)
			return err
		}), 0, nil
	case advisorKind + "/dismiss", advisorKind + "/track":
		to := "dismissed"
		if v.Value == "track" {
			to = "accepted"
		}
		return func() (int, error) {
			// The operator may have decided the recommendation since the open
			// check; accepted → dismissed is legal, so re-read before writing.
			cur, status, err := h.recStatusOf(ref)
			if err != nil {
				return status, err
			}
			if cur != "proposed" {
				return http.StatusConflict, errItemChanged
			}
			return h.recStatusAction(ref, to)
		}, 0, nil
	case advisorKind + "/fix-card":
		return h.planFixCard(v, ref)
	case advisorKind + "/improve":
		return func() (int, error) {
			moved, status, err := h.ensureRecAccepted(ref)
			if err != nil {
				return status, err
			}
			code, body := h.startImprove(ref)
			if code == http.StatusAccepted || code == http.StatusConflict {
				return 0, nil // generating, or a proposal is already open
			}
			msg, _ := body["error"].(string)
			if msg == "" {
				msg = "improve failed with status " + strconv.Itoa(code)
			}
			return h.undoRecAccept(ref, moved, code, errors.New(msg))
		}, 0, nil
	}
	return invalid("cannot accept a " + v.Kind + " verdict with value " + strconv.Quote(v.Value))
}

// lessonAction wraps a lessons call, mapping its errors onto HTTP statuses.
func lessonAction(fn func(now time.Time) error) acceptAction {
	return func() (int, error) {
		err := fn(time.Now())
		switch {
		case err == nil:
			return 0, nil
		case errors.Is(err, lessons.ErrNotFound):
			return http.StatusNotFound, err
		case errors.Is(err, lessons.ErrState):
			return http.StatusConflict, err
		case errors.Is(err, lessons.ErrInvalid):
			return http.StatusUnprocessableEntity, err
		}
		return http.StatusInternalServerError, err
	}
}

// recStatusAction is setRecommendationStatus where only a 2xx is success.
func (h *Handler) recStatusAction(id int64, to string) (int, error) {
	_, status, err := h.setRecommendationStatus(id, to)
	if status >= 200 && status < 300 {
		return 0, nil
	}
	if err == nil {
		err = fmt.Errorf("recommendation %d: status %d", id, status)
	}
	return status, err
}

// recStatusOf reads recommendation id's status.
func (h *Handler) recStatusOf(id int64) (string, int, error) {
	var status string
	err := h.DB.QueryRow(`SELECT status FROM recommendations WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", http.StatusNotFound, errors.New("recommendation not found")
	}
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	return status, 0, nil
}

// ensureRecAccepted moves recommendation id to accepted unless it already is;
// moved reports whether THIS call moved it (undoRecAccept puts only that back).
func (h *Handler) ensureRecAccepted(id int64) (moved bool, status int, err error) {
	cur, status, err := h.recStatusOf(id)
	if err != nil {
		return false, status, err
	}
	if cur == "accepted" {
		return false, 0, nil
	}
	if status, err := h.recStatusAction(id, "accepted"); err != nil {
		return false, status, err
	}
	return true, 0, nil
}

// undoRecAccept compensates an ensureRecAccepted whose follow-up step failed
// with (status, cause): when this request moved recommendation id to accepted,
// it goes back to proposed so the suggestion stays retryable. Direct SQL on
// purpose (like the advisor source's Undo): accepted → proposed must stay
// illegal for the operator's PATCH endpoint.
func (h *Handler) undoRecAccept(id int64, moved bool, status int, cause error) (int, error) {
	if !moved {
		return status, cause
	}
	nowS := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	res, err := h.DB.Exec(`UPDATE recommendations SET status='proposed', baseline=NULL, updated_at=?
		WHERE id=? AND status='accepted'`, nowS, id)
	var n int64
	if err == nil {
		n, err = res.RowsAffected()
	}
	if err == nil && n == 0 {
		err = errors.New("it is no longer accepted")
	}
	if err != nil {
		log.Printf("triage: recommendation %d: action failed and putting it back to proposed failed: %v", id, err)
		return http.StatusInternalServerError,
			fmt.Errorf("%w; the recommendation %d was left accepted (undo failed: %v)", cause, id, err)
	}
	return status, cause
}

// planFixCard validates a fix-card suggestion: the project comes from the
// verdict (never the payload), the payload is untrusted and only its title and
// prompt are read, both cut to size.
func (h *Handler) planFixCard(v triage.Verdict, recID int64) (acceptAction, int, error) {
	invalid := func(msg string) (acceptAction, int, error) {
		return nil, http.StatusUnprocessableEntity, errors.New(msg)
	}
	if v.ProjectID == nil || *v.ProjectID == 0 {
		return invalid("this recommendation has no project to put a card in")
	}
	var p map[string]any
	if err := json.Unmarshal(v.Payload, &p); err != nil || p == nil {
		return invalid("the suggestion's payload is not a JSON object")
	}
	title, _ := p["title"].(string)
	prompt, _ := p["prompt"].(string)
	title, prompt = strings.TrimSpace(title), strings.TrimSpace(prompt)
	if title == "" {
		return invalid("the suggestion's payload has no card title")
	}
	if prompt == "" {
		return invalid("the suggestion's payload has no card prompt")
	}
	title, prompt = cutRunes(title, cardTitleMaxRunes), cutBytes(prompt, cardPromptMaxBytes)

	var rule, recTitle string
	err := h.DB.QueryRow(`SELECT rule, title FROM recommendations WHERE id = ?`, recID).Scan(&rule, &recTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("recommendation not found")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	cardPrompt := prompt + fmt.Sprintf("\n\nRecommendation #%d (%s): %s", recID, rule, recTitle)
	projectID := *v.ProjectID

	return func() (int, error) {
		moved, status, err := h.ensureRecAccepted(recID)
		if err != nil {
			return status, err
		}
		cardID, err := NewRoutinesTaskCreator(h.DB).CreateTask(projectID, title, cardPrompt, "todo")
		if err != nil {
			return h.undoRecAccept(recID, moved, http.StatusInternalServerError, fmt.Errorf("create card: %w", err))
		}
		// A fresh object: nothing else the model wrote survives the accept.
		b, err := json.Marshal(map[string]string{"title": title, "prompt": prompt, "cardId": cardID})
		if err == nil {
			err = triageSvc.SetVerdictPayload(v.ID, b)
		}
		if err != nil {
			// The card exists: reopening would let a retry make a second one, so
			// the accept stands and only the bookkeeping is lost.
			log.Printf("triage: verdict %d: card %s created but not recorded: %v", v.ID, cardID, err)
		}
		return 0, nil
	}, 0, nil
}

// cutRunes keeps at most n runes of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// cutBytes keeps at most n bytes of s without splitting a rune.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
