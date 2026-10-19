package link

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Swarm kinds (mods-swarmery-link phase 6): sessions running the link mod
// report their $.session.send / session.receive traffic and the files each
// turn starts editing. Every one also lands in link_events like any other
// link kind (the dedup ledger); the side tables are link_messages and
// link_claims (migration 0116). Contract: docs/mods-link-protocol.md, Swarm.
const (
	KindSessionSend    = "session.send"
	KindSessionReceive = "session.receive"
	KindClaim          = "claim"
	KindRelease        = "release"
	KindConflict       = "conflict"
)

// link_messages.kind values.
const (
	MsgSend      = "send"
	MsgReceive   = "receive"
	MsgDashboard = "dashboard"
)

const (
	// MaxMessageBody bounds a stored message body, in characters; a longer one
	// is cut and stored with truncated = 1.
	MaxMessageBody = 4096
	// CorrelateWindow: a receive pairs with a send whose at is at most this far
	// from its own (either side may be ingested first).
	CorrelateWindow = 120 * time.Second
	// ClaimLiveFor: an unreleased claim older than this no longer counts as
	// live — its session crashed or lost its mod mid-turn.
	ClaimLiveFor = 30 * time.Minute

	maxAddress      = 512  // a send's `to`, in characters
	maxOriginKind   = 64   // a receive's originKind, in characters
	maxClaimPath    = 4096 // one claimed path, in bytes
	maxReleasePaths = 500  // paths in one release
)

// ErrBadMessage: a dashboard message is out of contract (empty text or
// recipient, text over MaxMessageBody, recipient == sender).
var ErrBadMessage = errors.New("bad message")

// dbtx is what the swarm helpers need from a *sql.DB or a *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MessageRecord is one message to store. Empty strings are SQL NULL.
type MessageRecord struct {
	Kind        string // MsgSend | MsgReceive | MsgDashboard
	FromSession string // sender's session uuid
	ToSession   string // recipient's session uuid; resolved from ToAddress when empty
	ToAddress   string // the recipient as the engine spelled it
	Body        string
	Truncated   bool
	Origin      string
	At          string // tsFormat
	Delivered   *bool
	Reason      string
	CommandID   int64
	// FromPID: a receive's sender process id, when the engine named it (the
	// mod's fromPid, else the envelope's uds socket). Used to find the
	// sender; not stored.
	FromPID int64
}

// swarmEvent is a validated swarm event, ready to apply.
type swarmEvent struct {
	msg        *MessageRecord // session.send / session.receive
	claim      string         // claim: the cleaned path
	release    bool           // release
	releaseAll bool
	paths      []string // release: cleaned paths (unless releaseAll)
}

// validateSwarm checks a swarm event's payload; a failure rejects that one
// event. It returns nil for every other kind.
func validateSwarm(kind, sessionUUID, at string, raw []byte) (*swarmEvent, error) {
	switch kind {
	case KindSessionSend, KindSessionReceive, KindClaim, KindRelease, KindConflict:
	default:
		return nil, nil
	}
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%s payload: %s", kind, fmt.Sprintf(format, a...))
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, bad("required")
	}
	switch kind {
	case KindSessionSend:
		var p struct {
			To          string     `json:"to"`
			Text        *string    `json:"text"`
			Truncated   bool       `json:"truncated"`
			IsDelivered *bool      `json:"isDelivered"`
			Reason      string     `json:"reason"`
			Origin      string     `json:"origin"`
			CommandID   *commandID `json:"commandId"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad("%v", err)
		}
		to := strings.TrimSpace(p.To)
		switch {
		case to == "":
			return nil, bad("to is required")
		case utf8.RuneCountInString(to) > maxAddress:
			return nil, bad("to exceeds %d characters", maxAddress)
		case p.Text == nil:
			return nil, bad("text is required")
		case p.Origin != "" && p.Origin != "model" && p.Origin != "plugin":
			return nil, bad("origin must be model or plugin")
		}
		rec := &MessageRecord{
			Kind: MsgSend, FromSession: sessionUUID, ToAddress: to, Body: *p.Text,
			Truncated: p.Truncated, Origin: p.Origin, At: at, Delivered: p.IsDelivered,
		}
		if p.IsDelivered != nil && !*p.IsDelivered {
			rec.Reason = p.Reason
		}
		if p.CommandID != nil {
			rec.CommandID = int64(*p.CommandID)
		}
		return &swarmEvent{msg: rec}, nil
	case KindSessionReceive:
		var p struct {
			Text       *string `json:"text"`
			Truncated  bool    `json:"truncated"`
			OriginKind string  `json:"originKind"`
			FromPlugin string  `json:"fromPlugin"`
			FromPID    *int64  `json:"fromPid"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad("%v", err)
		}
		switch {
		case p.Text == nil:
			return nil, bad("text is required")
		case utf8.RuneCountInString(p.OriginKind) > maxOriginKind:
			return nil, bad("originKind exceeds %d characters", maxOriginKind)
		case p.FromPID != nil && *p.FromPID <= 0:
			return nil, bad("fromPid must be a positive integer")
		}
		// Stored without the engine's envelope, so it compares with the send
		// and reads as the sender wrote it (a mod before 0.6.2 sends it as
		// the engine delivered it).
		body, from, _ := unwrapEnvelope(*p.Text, p.Truncated)
		pid := socketPID(from)
		if p.FromPID != nil {
			pid = *p.FromPID
		}
		return &swarmEvent{msg: &MessageRecord{
			Kind: MsgReceive, ToSession: sessionUUID, Body: body,
			Truncated: p.Truncated, Origin: p.OriginKind, At: at, FromPID: pid,
		}}, nil
	case KindClaim:
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad("%v", err)
		}
		path, err := cleanAbs(p.Path)
		if err != nil {
			return nil, bad("path %v", err)
		}
		return &swarmEvent{claim: path}, nil
	case KindRelease:
		var p struct {
			Paths []string `json:"paths"`
			All   bool     `json:"all"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad("%v", err)
		}
		if p.All {
			return &swarmEvent{release: true, releaseAll: true}, nil
		}
		switch {
		case len(p.Paths) == 0:
			return nil, bad("paths or all is required")
		case len(p.Paths) > maxReleasePaths:
			return nil, bad("paths exceeds %d entries", maxReleasePaths)
		}
		out := make([]string, 0, len(p.Paths))
		for i, raw := range p.Paths {
			path, err := cleanAbs(raw)
			if err != nil {
				return nil, bad("paths[%d] %v", i, err)
			}
			out = append(out, path)
		}
		return &swarmEvent{release: true, paths: out}, nil
	default: // KindConflict: advisory, kept in link_events only
		var p struct {
			Path         string `json:"path"`
			OtherSession string `json:"otherSession"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, bad("%v", err)
		}
		if _, err := cleanAbs(p.Path); err != nil {
			return nil, bad("path %v", err)
		}
		if strings.TrimSpace(p.OtherSession) == "" {
			return nil, bad("otherSession is required")
		}
		return &swarmEvent{}, nil
	}
}

// errCommandID is a session.send commandId that is not a positive integer.
var errCommandID = errors.New("commandId must be a positive integer")

// commandID is a session.send's commandId: a JSON number, or a string of
// decimal digits — mods up to the phase-6 fix sent the id as the string the
// command poll handed them, and such a send must still fold rather than be
// rejected. Anything else (zero, negative, fractional, exponent, other text)
// fails with errCommandID.
type commandID int64

func (c *commandID) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return errCommandID
		}
	}
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return errCommandID
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return errCommandID
	}
	*c = commandID(n)
	return nil
}

// cleanAbs requires an absolute path and returns it cleaned.
func cleanAbs(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("is required")
	case len(p) > maxClaimPath:
		return "", fmt.Errorf("exceeds %d bytes", maxClaimPath)
	case !filepath.IsAbs(p):
		return "", errors.New("must be absolute")
	}
	return filepath.Clean(p), nil
}

// applySwarm stores a stored swarm event's side effect inside the batch
// transaction. msg reports a link_messages row stored; claims reports the
// live claims set changed.
func applySwarm(ctx context.Context, tx *sql.Tx, n normalized) (msg, claims bool, err error) {
	sw := n.swarm
	switch {
	case sw == nil:
		return false, false, nil
	case sw.msg != nil:
		if _, err := RecordMessage(ctx, tx, *sw.msg); err != nil {
			return false, false, err
		}
		return true, false, nil
	case sw.claim != "":
		changed, err := UpsertClaim(ctx, tx, n.SessionID, sw.claim, n.at)
		return false, changed, err
	case sw.release:
		released, err := ReleaseClaims(ctx, tx, n.SessionID, sw.paths, sw.releaseAll, n.at)
		return false, released > 0, err
	}
	return false, false, nil
}

// clipBody cuts body to MaxMessageBody characters; cut reports it did.
func clipBody(body string) (string, bool) {
	if utf8.RuneCountInString(body) <= MaxMessageBody {
		return body, false
	}
	n := 0
	for i := range body {
		if n == MaxMessageBody {
			return body[:i], true
		}
		n++
	}
	return body, false
}

// The engine (Claude Code 2.1.303) hands the receiver a peer's message wrapped
// in an envelope naming the sender, while the sender's session.send reports
// the bare text:
//
//	<cross-session-message from="uds:/tmp/cc-socks/41881.sock" from-name="proj-10" from-mode="bypass">
//	ping-from-A
//	</cross-session-message>
var (
	envelopeOpen  = regexp.MustCompile(`^\s*<cross-session-message((?:\s+[^\s=>"']+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*>`)
	envelopeClose = regexp.MustCompile(`</cross-session-message\s*>\s*$`)
	envelopeAttr  = regexp.MustCompile(`([^\s=>"']+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// socketName: the engine names a session's messaging socket after its
	// process id (the envelope's from and the delivery's verifiedPeerPid
	// agree on it).
	socketName = regexp.MustCompile(`^uds:.*/([0-9]+)\.sock$`)
)

// unwrapEnvelope returns text without the engine's cross-session envelope:
// the body between the tags with the whitespace around it trimmed, and the
// envelope's from attribute. Attributes may come in any order, with others
// among them. A text that is not one whole envelope comes back unchanged with
// ok false — except a truncated one, which may have lost its closing tag.
func unwrapEnvelope(text string, truncated bool) (body, from string, ok bool) {
	open := envelopeOpen.FindStringSubmatchIndex(text)
	if open == nil {
		return text, "", false
	}
	rest := text[open[1]:]
	if end := envelopeClose.FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	} else if !truncated {
		return text, "", false
	}
	for _, a := range envelopeAttr.FindAllStringSubmatch(text[open[2]:open[3]], -1) {
		if a[1] == "from" {
			from = a[2] + a[3] // one of the two quote forms matched
		}
	}
	return strings.TrimSpace(rest), from, true
}

// socketPID is the process id an envelope's from address names, or 0.
func socketPID(from string) int64 {
	m := socketName.FindStringSubmatch(from)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// matchKey is what correlation compares: the sha-256 of a body without an
// envelope (rows stored before receives were unwrapped still carry one) and
// without the whitespace around it.
func matchKey(body string) [32]byte {
	b, _, _ := unwrapEnvelope(body, false)
	return sha256.Sum256([]byte(strings.TrimSpace(b)))
}

// RecordMessage stores one message and correlates it with the other side of
// its pair, returning the link_messages id it landed on.
//
//   - A send that ran a dashboard 'message' command (CommandID set) folds into
//     that command's dashboard row instead of adding a second row for the same
//     message: the row takes the engine's address, truncation and origin. Its
//     delivered is the ack's to set (Commands.Ack) — unless the ack came
//     first, when the send's isDelivered overrides it (a late send the mod
//     timed out on). Without such a row it is stored as a plain send carrying
//     the command id.
//   - ToSession is resolved from ToAddress when the address is one of our
//     session uuids.
//   - A receive is paired with the most recent unpaired send / dashboard row
//     addressed to the receiver (by session uuid; failing that, one whose
//     address resolved to no session — a name or an agent id — sent from a
//     session in the receiver's project) with the same text (matchKey) within
//     CorrelateWindow, preferring one sent from the session whose pid is the
//     receive's FromPID: the send becomes delivered, the receive gets the
//     sender as from_session. With no match, a FromPID that names a session
//     still sets from_session (matched_id stays NULL); otherwise it stays
//     NULL. A send arriving after its receive pairs the same way, never with
//     a receive whose from_session names another session.
func RecordMessage(ctx context.Context, ex dbtx, rec MessageRecord) (int64, error) {
	body, cut := clipBody(rec.Body)
	rec.Body, rec.Truncated = body, rec.Truncated || cut

	if rec.Kind == MsgSend && rec.CommandID > 0 {
		id, err := foldDashboardSend(ctx, ex, rec)
		if err != nil || id != 0 {
			return id, err
		}
	}
	if rec.ToSession == "" && rec.ToAddress != "" {
		var uuid string
		err := ex.QueryRowContext(ctx, `SELECT session_uuid FROM sessions WHERE session_uuid = ?`, rec.ToAddress).Scan(&uuid)
		switch {
		case err == nil:
			rec.ToSession = uuid
		case !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("link swarm: resolve recipient: %w", err)
		}
	}
	var delivered any
	if rec.Delivered != nil {
		delivered = boolInt(*rec.Delivered)
	}
	var cmd any
	if rec.CommandID > 0 {
		cmd = rec.CommandID
	}
	r, err := ex.ExecContext(ctx,
		`INSERT INTO link_messages (kind, from_session, to_session, to_address, body, truncated, origin, at, delivered, reason, command_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Kind, nullable(rec.FromSession), nullable(rec.ToSession), nullable(rec.ToAddress), rec.Body,
		boolInt(rec.Truncated), nullable(rec.Origin), rec.At, delivered, nullable(rec.Reason), cmd)
	if err != nil {
		return 0, fmt.Errorf("link swarm: insert message: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("link swarm: insert message: %w", err)
	}
	switch rec.Kind {
	case MsgReceive:
		err = correlateReceive(ctx, ex, id, rec)
	case MsgSend:
		err = correlateSend(ctx, ex, id, rec)
	}
	return id, err
}

// foldDashboardSend merges a mod's session.send for a dashboard command into
// that command's dashboard row; 0 when there is no such row.
func foldDashboardSend(ctx context.Context, ex dbtx, rec MessageRecord) (int64, error) {
	var (
		id        int64
		matched   sql.NullInt64
		delivered sql.NullInt64
		toSess    sql.NullString
	)
	err := ex.QueryRowContext(ctx,
		`SELECT id, matched_id, delivered, to_session FROM link_messages
		  WHERE kind = 'dashboard' AND command_id = ? AND from_session = ?
		  ORDER BY id LIMIT 1`, rec.CommandID, rec.FromSession).Scan(&id, &matched, &delivered, &toSess)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("link swarm: find dashboard message: %w", err)
	}
	if _, err := ex.ExecContext(ctx,
		`UPDATE link_messages
		    SET to_address = ?, truncated = MAX(truncated, ?), origin = COALESCE(origin, ?)
		  WHERE id = ?`, rec.ToAddress, boolInt(rec.Truncated), nullable(rec.Origin), id); err != nil {
		return 0, fmt.Errorf("link swarm: fold dashboard message: %w", err)
	}
	// The ack already settled the row, and the send reports how it really
	// ended: the engine's verdict wins. That is the mod's message timeout — it
	// acks failed after 10 s, and the send it gave up on may settle later.
	// Delivered clears the reason; not delivered takes the engine's reason in
	// the ack's spelling, or keeps the ack's when the engine gave none.
	if delivered.Valid && rec.Delivered != nil {
		var reason any
		if rec.Reason != "" {
			reason = "undeliverable: " + rec.Reason
		}
		if _, err := ex.ExecContext(ctx,
			`UPDATE link_messages
			    SET delivered = ?, reason = CASE WHEN ? = 1 THEN NULL ELSE COALESCE(?, reason) END
			  WHERE id = ?`,
			boolInt(*rec.Delivered), boolInt(*rec.Delivered), reason, id); err != nil {
			return 0, fmt.Errorf("link swarm: fold dashboard message: %w", err)
		}
	}
	if !matched.Valid {
		rec.ToSession = toSess.String
		if err := correlateSend(ctx, ex, id, rec); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// candidate is one row that may pair with a message.
type candidate struct {
	id   int64
	body string
	from sql.NullString
	to   sql.NullString
	pid  sql.NullInt64 // a send's: its sender's sessions.pid
}

// correlateReceive pairs a just-stored receive with its send (see
// RecordMessage).
func correlateReceive(ctx context.Context, ex dbtx, recvID int64, rec MessageRecord) error {
	lo, hi, err := window(rec.At)
	if err != nil {
		return err
	}
	receiver := rec.ToSession
	// The unresolved-address fallback (to_session NULL: a name or an agent id)
	// only takes a send from a session in the receiver's own project: a name
	// says nothing about which project's session it reached.
	cands, err := candidates(ctx, ex,
		`SELECT m.id, m.body, m.from_session, m.to_session,
		        (SELECT s.pid FROM sessions s WHERE s.session_uuid = m.from_session)
		   FROM link_messages m
		  WHERE m.kind IN ('send', 'dashboard') AND m.matched_id IS NULL
		    AND (m.to_session = ? OR m.to_address = ?
		         OR (m.to_session IS NULL AND EXISTS (
		             SELECT 1 FROM sessions fs JOIN sessions rs ON rs.project_id = fs.project_id
		              WHERE fs.session_uuid = m.from_session AND rs.session_uuid = ?)))
		    AND COALESCE(m.from_session, '') <> ?
		    AND m.at >= ? AND m.at <= ?
		  ORDER BY CASE WHEN m.to_session = ? OR m.to_address = ? THEN 0 ELSE 1 END, m.at DESC, m.id DESC`,
		receiver, receiver, receiver, receiver, lo, hi, receiver, receiver)
	if err != nil {
		return err
	}
	// The engine names the sending process: a send from a session known to
	// run as another process is not this message, and one from that very
	// process wins over the order above.
	key := matchKey(rec.Body)
	var match *candidate
	for i := range cands {
		c := &cands[i]
		if matchKey(c.body) != key {
			continue
		}
		if rec.FromPID > 0 && c.pid.Valid {
			if c.pid.Int64 != rec.FromPID {
				continue
			}
			match = c
			break
		}
		if match == nil {
			match = c
		}
	}
	if match != nil {
		return pair(ctx, ex, match.id, recvID, match.from.String, receiver)
	}
	return attributeByPID(ctx, ex, recvID, rec.FromPID, receiver)
}

// attributeByPID sets an unpaired receive's from_session to the session whose
// process sent it, when one is known: the newest session with that pid that
// procwatch has not seen die. matched_id stays NULL — a send arriving later
// from that session still pairs (correlateSend).
func attributeByPID(ctx context.Context, ex dbtx, recvID, pid int64, receiver string) error {
	if pid <= 0 {
		return nil
	}
	var sender string
	err := ex.QueryRowContext(ctx,
		`SELECT session_uuid FROM sessions
		  WHERE pid = ? AND session_uuid <> ? AND COALESCE(proc_state, '') <> 'dead'
		  ORDER BY started_at DESC, id DESC LIMIT 1`, pid, receiver).Scan(&sender)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("link swarm: sender by pid: %w", err)
	}
	if _, err := ex.ExecContext(ctx,
		`UPDATE link_messages SET from_session = ? WHERE id = ?`, sender, recvID); err != nil {
		return fmt.Errorf("link swarm: sender by pid: %w", err)
	}
	return nil
}

// correlateSend pairs a just-stored (or folded) send with a receive that was
// ingested before it (see RecordMessage).
func correlateSend(ctx context.Context, ex dbtx, sendID int64, rec MessageRecord) error {
	if rec.FromSession == "" {
		return nil
	}
	lo, hi, err := window(rec.At)
	if err != nil {
		return err
	}
	// A send addressed to a session pairs with that session's receive alone.
	// One whose address resolved to no session (a name, an agent id) pairs
	// with a receive whose session the address spells, else with one in the
	// sender's own project — never across projects. A receive already
	// attributed to a sender by its pid (attributeByPID) is that sender's
	// alone, and comes first.
	cands, err := candidates(ctx, ex,
		`SELECT r.id, r.body, r.from_session, r.to_session, NULL FROM link_messages r
		  WHERE r.kind = 'receive' AND r.matched_id IS NULL
		    AND (r.from_session IS NULL OR r.from_session = ?)
		    AND r.to_session <> ?
		    AND CASE WHEN ? <> '' THEN r.to_session = ?
		             ELSE r.to_session = ? OR EXISTS (
		                  SELECT 1 FROM sessions rs JOIN sessions ss ON ss.project_id = rs.project_id
		                   WHERE rs.session_uuid = r.to_session AND ss.session_uuid = ?) END
		    AND r.at >= ? AND r.at <= ?
		  ORDER BY CASE WHEN r.from_session = ? THEN 0 ELSE 1 END,
		           CASE WHEN r.to_session = ? THEN 0 ELSE 1 END, r.at, r.id`,
		rec.FromSession, rec.FromSession, rec.ToSession, rec.ToSession, rec.ToAddress, rec.FromSession, lo, hi,
		rec.FromSession, rec.ToAddress)
	if err != nil {
		return err
	}
	key := matchKey(rec.Body)
	for _, c := range cands {
		if matchKey(c.body) == key {
			return pair(ctx, ex, sendID, c.id, rec.FromSession, c.to.String)
		}
	}
	return nil
}

// candidates reads every row of q before returning, so the caller may write
// on the same connection (the store runs one).
func candidates(ctx context.Context, ex dbtx, q string, args ...any) ([]candidate, error) {
	rows, err := ex.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("link swarm: correlate: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.body, &c.from, &c.to, &c.pid); err != nil {
			return nil, fmt.Errorf("link swarm: correlate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("link swarm: correlate: %w", err)
	}
	return out, nil
}

// pair links a send / dashboard row and a receive row. A send becomes
// delivered; a dashboard row's delivered is the ack's (Commands.Ack).
func pair(ctx context.Context, ex dbtx, sendID, recvID int64, sender, receiver string) error {
	if _, err := ex.ExecContext(ctx,
		`UPDATE link_messages
		    SET matched_id = ?, to_session = COALESCE(to_session, ?),
		        delivered = CASE WHEN kind = 'dashboard' THEN delivered ELSE 1 END
		  WHERE id = ?`, recvID, nullable(receiver), sendID); err != nil {
		return fmt.Errorf("link swarm: pair: %w", err)
	}
	if _, err := ex.ExecContext(ctx,
		`UPDATE link_messages SET matched_id = ?, from_session = ? WHERE id = ?`,
		sendID, nullable(sender), recvID); err != nil {
		return fmt.Errorf("link swarm: pair: %w", err)
	}
	return nil
}

// Stamp formats t the way every link timestamp is stored (fixed-width ms
// UTC), e.g. for a ListMessages since bound.
func Stamp(t time.Time) string { return stamp(t) }

// window returns at ± CorrelateWindow in tsFormat.
func window(at string) (lo, hi string, err error) {
	t, err := time.Parse(tsFormat, at)
	if err != nil {
		return "", "", fmt.Errorf("link swarm: at %q: %w", at, err)
	}
	return stamp(t.Add(-CorrelateWindow)), stamp(t.Add(CorrelateWindow)), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SessionRef names a session in a swarm read: Title is what the sessions list
// shows (COALESCE(custom_title, title), as in internal/api handlers.go).
type SessionRef struct {
	ID    int64   `json:"id"`
	UUID  string  `json:"uuid"`
	Title *string `json:"title"`
}

// Message is one link_messages row as GET /api/swarm/messages serves it.
type Message struct {
	ID          int64       `json:"id"`
	Kind        string      `json:"kind"`
	FromSession *SessionRef `json:"fromSession"`
	ToSession   *SessionRef `json:"toSession"`
	ToAddress   *string     `json:"toAddress"`
	Body        string      `json:"body"`
	Truncated   bool        `json:"truncated"`
	Origin      *string     `json:"origin"`
	At          string      `json:"at"`
	Delivered   *bool       `json:"delivered"`
	Reason      *string     `json:"reason"`
}

// ListMessages returns up to limit messages, newest first. projectID 0 is
// every project; otherwise a message is listed when either side's session
// belongs to it. since ("" = no bound) is a tsFormat lower bound on at.
func ListMessages(ctx context.Context, q dbtx, projectID int64, since string, limit int) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT m.id, m.kind, m.to_address, m.body, m.truncated, m.origin, m.at, m.delivered, m.reason,
		        fs.id, fs.session_uuid, COALESCE(fs.custom_title, fs.title),
		        ts.id, ts.session_uuid, COALESCE(ts.custom_title, ts.title)
		   FROM link_messages m
		   LEFT JOIN sessions fs ON fs.session_uuid = m.from_session
		   LEFT JOIN sessions ts ON ts.session_uuid = m.to_session
		  WHERE (? = 0 OR fs.project_id = ? OR ts.project_id = ?)
		    AND (? = '' OR m.at >= ?)
		  ORDER BY m.at DESC, m.id DESC
		  LIMIT ?`, projectID, projectID, projectID, since, since, limit)
	if err != nil {
		return nil, fmt.Errorf("link swarm: list messages: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var (
			m                      Message
			toAddr, origin, reason sql.NullString
			truncated              int
			delivered              sql.NullInt64
			fID, tID               sql.NullInt64
			fUUID, tUUID           sql.NullString
			fTitle, tTitle         sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.Kind, &toAddr, &m.Body, &truncated, &origin, &m.At, &delivered, &reason,
			&fID, &fUUID, &fTitle, &tID, &tUUID, &tTitle); err != nil {
			return nil, fmt.Errorf("link swarm: list messages: %w", err)
		}
		m.ToAddress, m.Origin, m.Reason = strPtr(toAddr), strPtr(origin), strPtr(reason)
		m.Truncated = truncated != 0
		if delivered.Valid {
			d := delivered.Int64 != 0
			m.Delivered = &d
		}
		m.FromSession = sessionRef(fID, fUUID, fTitle)
		m.ToSession = sessionRef(tID, tUUID, tTitle)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("link swarm: list messages: %w", err)
	}
	return out, nil
}

func sessionRef(id sql.NullInt64, uuid, title sql.NullString) *SessionRef {
	if !id.Valid {
		return nil
	}
	return &SessionRef{ID: id.Int64, UUID: uuid.String, Title: strPtr(title)}
}

func strPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// UpsertClaim records that the session (by uuid) started editing path at at.
// A new claim is inserted; a released one is re-opened (released_at cleared,
// claimed_at refreshed) when at is after its release; a live one has its
// claimed_at moved forward. An older at than the row already holds changes
// nothing, so a late or resent claim never re-opens a newer release.
// changed reports a row was written.
func UpsertClaim(ctx context.Context, ex dbtx, sessionUUID, path, at string) (bool, error) {
	r, err := ex.ExecContext(ctx,
		`INSERT INTO link_claims (session_id, path, claimed_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET claimed_at = excluded.claimed_at, released_at = NULL
		  WHERE (link_claims.released_at IS NOT NULL AND excluded.claimed_at > link_claims.released_at)
		     OR (link_claims.released_at IS NULL AND excluded.claimed_at > link_claims.claimed_at)`,
		sessionUUID, path, at)
	if err != nil {
		return false, fmt.Errorf("link swarm: claim: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("link swarm: claim: %w", err)
	}
	return n > 0, nil
}

// ReleaseClaims releases the session's live claims on paths (or every one
// with all) that were claimed at or before at, and returns how many.
func ReleaseClaims(ctx context.Context, ex dbtx, sessionUUID string, paths []string, all bool, at string) (int64, error) {
	if !all && len(paths) == 0 {
		return 0, nil
	}
	q := `UPDATE link_claims SET released_at = ?
	       WHERE session_id = ? AND released_at IS NULL AND claimed_at <= ?`
	args := []any{at, sessionUUID, at}
	if !all {
		q += ` AND path IN (?` + strings.Repeat(`, ?`, len(paths)-1) + `)`
		for _, p := range paths {
			args = append(args, p)
		}
	}
	r, err := ex.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("link swarm: release: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("link swarm: release: %w", err)
	}
	return n, nil
}

// Claim is one live claim as GET /api/swarm/claims serves it.
type Claim struct {
	SessionID   int64  `json:"sessionId"`
	SessionUUID string `json:"sessionUuid"`
	Path        string `json:"path"`
	ClaimedAt   string `json:"claimedAt"`
	projectID   int64
}

// ConflictSession is one holder of a conflicted path.
type ConflictSession struct {
	SessionID   int64  `json:"sessionId"`
	SessionUUID string `json:"sessionUuid"`
}

// Conflict is a path two or more sessions hold a live claim on.
type Conflict struct {
	Path     string            `json:"path"`
	Sessions []ConflictSession `json:"sessions"`
}

// liveClaims reads every live claim (released_at NULL, claimed_at within
// ClaimLiveFor of now) whose session row exists, ordered by path.
func liveClaims(ctx context.Context, q dbtx, now time.Time) ([]Claim, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT s.id, c.session_id, c.path, c.claimed_at, s.project_id
		   FROM link_claims c JOIN sessions s ON s.session_uuid = c.session_id
		  WHERE c.released_at IS NULL AND c.claimed_at > ?
		  ORDER BY c.path, c.claimed_at, s.id`, stamp(now.Add(-ClaimLiveFor)))
	if err != nil {
		return nil, fmt.Errorf("link swarm: claims: %w", err)
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.SessionID, &c.SessionUUID, &c.Path, &c.ClaimedAt, &c.projectID); err != nil {
			return nil, fmt.Errorf("link swarm: claims: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("link swarm: claims: %w", err)
	}
	return out, nil
}

// LiveClaims returns the live claims of projectID's sessions (0 = all
// projects), ordered by path.
func LiveClaims(ctx context.Context, q dbtx, projectID int64, now time.Time) ([]Claim, error) {
	all, err := liveClaims(ctx, q, now)
	if err != nil {
		return nil, err
	}
	out := []Claim{}
	for _, c := range all {
		if projectID == 0 || c.projectID == projectID {
			out = append(out, c)
		}
	}
	return out, nil
}

// Conflicts returns every path two or more sessions hold a live claim on,
// ordered by path. With projectID != 0 a conflict is kept when any of its
// sessions belongs to that project, and still lists every holder. Advisory:
// nothing is blocked on it.
func Conflicts(ctx context.Context, q dbtx, projectID int64, now time.Time) ([]Conflict, error) {
	all, err := liveClaims(ctx, q, now)
	if err != nil {
		return nil, err
	}
	out := []Conflict{}
	for i := 0; i < len(all); {
		j := i
		inProject := false
		for j < len(all) && all[j].Path == all[i].Path {
			inProject = inProject || projectID == 0 || all[j].projectID == projectID
			j++
		}
		if j-i >= 2 && inProject {
			c := Conflict{Path: all[i].Path, Sessions: make([]ConflictSession, 0, j-i)}
			for _, h := range all[i:j] {
				c.Sessions = append(c.Sessions, ConflictSession{SessionID: h.SessionID, SessionUUID: h.SessionUUID})
			}
			out = append(out, c)
		}
		i = j
	}
	return out, nil
}
