// The swarmery link endpoints (mods-swarmery-link phase 1): a Claude Code mod
// POSTs session events here. Contract: docs/mods-link-protocol.md.
//
// Unlike every other write in this API the /api/link/* routes are NOT behind
// requireLocalOrigin: the mod sends no Origin, and the routes carry their own
// credential instead — a request signed with the link token (linkAuth below),
// or the legacy bearer form checked by link.Holder.Require. The Host fence
// (requireLocalHost, routes.go) still applies, because it wraps the whole
// inner mux.
//
// The link is attached as package state (same pattern as AttachApprovals /
// AttachBus) so a bare &Handler{DB: db} in tests keeps working: with nothing
// attached both routes answer 503 {"error":"link disabled"}.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

const (
	// maxLinkBody bounds one POST /api/link/events body (http.MaxBytesReader →
	// 413). A batch of 200 events with measure payloads is a few tens of KiB.
	maxLinkBody = 1 << 20
	// maxLinkEvents bounds one batch; a larger one is 413 as well — the mod
	// splits, it never needs a bigger single request.
	maxLinkEvents = 200
	// rejectExcluded is the per-event reason for an event whose cwd matches the
	// daemon's exclude list (the same list the JSONL ingest and the approvals
	// hook honour).
	rejectExcluded = "excluded project"
	// linkWriteBudget caps one batch's whole write once detached from the
	// request: the connection wait (link.AcquireTimeout) plus the
	// transaction.
	linkWriteBudget = 30 * time.Second
	// linkBusyRetryAfter is the Retry-After of the busy answer, in seconds:
	// the mod's flush cadence (client.ts FLUSH_EVERY_MS).
	linkBusyRetryAfter = "2"
)

var (
	// linkHolder serves the current link token; nil disables /api/link/*.
	linkHolder *link.Holder
	// linkExclude is the daemon's --exclude-projects list (the ingest.Config /
	// approvals.Options Exclude value), so the link mints no rows for a
	// throwaway cwd either.
	linkExclude ingest.ExcludeList
)

// AttachLink wires the link token holder and the exclude list into the
// /api/link/* endpoints (daemon startup). A nil holder disables them (503).
func AttachLink(h *link.Holder, exclude ingest.ExcludeList) {
	linkHolder = h
	linkExclude = exclude
}

// linkAuth guards a link handler: 503 while the link is not attached, then
// one of two schemes (docs/mods-link-protocol.md "Auth"):
//
//   - Swarmery-HMAC (internal/linksig): the request is signed with the token,
//     checked within the allowed skew and once per nonce, and the answer
//     carries X-Swarmery-Sig, the daemon's signature over the request's nonce,
//     the status and the body. Every in-repo client signs.
//   - the legacy bearer token, still accepted during the transition; its
//     answer is not signed. The first one a process sees is logged once.
//
// Either way a failure is 401 {"error":"link token required"}. The token is
// read through the holder's throttled reload, so a rotation needs no restart.
func linkAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := linkHolder
		if h == nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "link disabled"})
			return
		}
		if !linksig.IsSigned(r) {
			noteLegacyBearer(r)
			h.Require()(next)(w, r)
			return
		}
		key := h.Current()
		nonce, err := linkVerifier.VerifyRequest(r, key, time.Now())
		switch {
		case errors.Is(err, linksig.ErrBodyTooLarge):
			writeClientErr(w, http.StatusRequestEntityTooLarge, "body exceeds 4 MiB")
			return
		case err != nil:
			link.WriteUnauthorized(w)
			return
		}
		sw := &signingWriter{ResponseWriter: w}
		next(sw, r)
		sw.finish(key, nonce)
	}
}

// linkVerifier remembers the nonces of the signed requests this process
// accepted, so none is served twice.
var linkVerifier = linksig.NewVerifier()

// legacyBearerOnce logs the first bearer-authenticated link request of the
// process: a client that has not moved to signed requests yet.
var legacyBearerOnce sync.Once

func noteLegacyBearer(r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ") {
		return
	}
	legacyBearerOnce.Do(func() {
		log.Printf("link: a client used the legacy bearer token on %s %s; signed requests (Swarmery-HMAC) replace it", r.Method, r.URL.Path)
	})
}

// signingWriter holds a link handler's answer so it can be signed: the status
// and body are buffered, then written once with X-Swarmery-Sig. Link answers
// are small JSON bodies written once (no streaming, no Flush).
type signingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (s *signingWriter) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
}

func (s *signingWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.body.Write(p)
}

// finish signs and writes what the handler answered (200 with an empty body
// when it wrote nothing, as net/http would).
func (s *signingWriter) finish(key, nonce string) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	body := s.body.Bytes()
	s.ResponseWriter.Header().Set(linksig.ResponseHeader, linksig.SignResponse(key, nonce, s.status, body))
	s.ResponseWriter.WriteHeader(s.status)
	if len(body) > 0 {
		_, _ = s.ResponseWriter.Write(body)
	}
}

// hookAuth guards the `swarmery hook` shim routes (POST /api/hooks/
// permission-request, /stop, /session-start) with the same link token, sent
// in the Authorization header, as /api/link/*: before it, any local process
// that sent no Origin could open an approval there. Same answers as
// linkAuth: 503 {"error":"link disabled"} while no holder is attached, 401
// {"error":"link token required"} on a missing or wrong token.
//
// Not behind it: answering an approval (POST /api/approvals/{id}), still
// gated only by the Host/Origin fence (a known gap, tracked separately), and
// the routine webhook (/api/hooks/routine/…), which carries its own
// per-routine token.
func hookAuth(next http.HandlerFunc) http.HandlerFunc {
	return linkAuth(next)
}

// linkEventsRequest is the POST /api/link/events body. An envelope rather than
// a bare array so fields can be added without breaking the mod. Events is a
// pointer so an absent key (400) differs from an empty batch (202, all zero).
type linkEventsRequest struct {
	Events *[]link.Event `json:"events"`
}

// POST /api/link/events — store a batch of mod events.
//
//	202 link.Result   the batch was processed (per-event rejects are in the body)
//	400               body is not JSON, not the envelope, or has no "events"
//	401               missing / wrong bearer token
//	413               body > 1 MiB, or more than 200 events
//	500               database error; the whole batch was rolled back
//	503               link not attached
//	504 + Retry-After no store connection within link.AcquireTimeout; nothing
//	                  stored, resend the same batch
func (h *Handler) linkEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLinkBody)
	var req linkEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeClientErr(w, http.StatusRequestEntityTooLarge, "body exceeds 1 MiB")
			return
		}
		writeClientErr(w, http.StatusBadRequest, "malformed body: want {\"events\":[…]}")
		return
	}
	if req.Events == nil {
		writeClientErr(w, http.StatusBadRequest, "events is required")
		return
	}
	batch := *req.Events
	if len(batch) > maxLinkEvents {
		writeClientErr(w, http.StatusRequestEntityTooLarge, "batch exceeds 200 events")
		return
	}

	res, err := ingestLinkBatch(r, h, batch)
	if errors.Is(err, link.ErrBusy) {
		// Not 503: the mod reads 503 as "link disabled" and turns the link
		// off for the session (client.ts outcome). Any other 5xx is "retry".
		log.Printf("warning: api: %v", err)
		w.Header().Set("Retry-After", linkBusyRetryAfter)
		writeJSONStatus(w, http.StatusGatewayTimeout, map[string]string{"error": "database busy"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, res)
}

// ingestLinkBatch drops events from excluded cwds (counted as rejected) and
// hands the rest to link.Ingest, mapping its reject indexes back onto the
// caller's batch so every Reject.Index names the event the mod sent.
func ingestLinkBatch(r *http.Request, h *Handler, batch []link.Event) (link.Result, error) {
	var res link.Result
	kept := make([]link.Event, 0, len(batch))
	origin := make([]int, 0, len(batch)) // kept[i] was batch[origin[i]]
	for i, ev := range batch {
		if ev.Cwd != "" && linkExclude.MatchPath(ev.Cwd) {
			res.Rejected++
			res.Rejects = append(res.Rejects, link.Reject{Index: i, Reason: rejectExcluded})
			continue
		}
		kept = append(kept, ev)
		origin = append(origin, i)
	}
	if len(kept) == 0 {
		return res, nil
	}
	// The write runs on the daemon's budget, not the client's: a mod that gives
	// up at 5 s must not discard a batch already queued for the connection
	// (the live "link ingest: begin: context canceled"). link.Ingest bounds
	// its own wait for the connection (ErrBusy); linkWriteBudget only caps a
	// transaction that has the connection and still never ends.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), linkWriteBudget)
	defer cancel()
	in, err := link.Ingest(ctx, h.DB, publishLink, kept)
	if err != nil {
		return link.Result{}, err
	}
	wakeOnTurnEnd(kept)
	res.Accepted = in.Accepted
	res.Duplicates = in.Duplicates
	res.Rejected += in.Rejected
	for _, rj := range in.Rejects {
		res.Rejects = append(res.Rejects, link.Reject{Index: origin[rj.Index], Reason: rj.Reason})
	}
	return res, nil
}

// wakeOnTurnEnd wakes the command long-poll of every session the batch
// reported a turn.complete for: link.Ingest has just released that session's
// prompts held for a turn end (a spurious wake — a duplicate event — only
// costs the poll one look).
func wakeOnTurnEnd(batch []link.Event) {
	c := linkCommands
	if c == nil {
		return
	}
	for _, ev := range batch {
		if ev.Kind == link.KindTurnComplete && ev.SessionID != "" {
			c.Wake(ev.SessionID)
		}
	}
}

// publishLink forwards link.Ingest's notifications to the WS bus (a no-op when
// the bus is not attached, e.g. serve --no-ingest).
func publishLink(n ingest.Notification) {
	if wsBus != nil {
		wsBus.Publish(n)
	}
}

// GET /api/link/health — the mod's "is the daemon there and does my token
// work" probe: 200 {"ok":true,"daemon":"<build identity>"} behind the token.
func (h *Handler) linkHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "daemon": version.String()}, nil)
}
