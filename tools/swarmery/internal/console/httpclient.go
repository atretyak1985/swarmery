package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

// HTTPClient talks to a running swarmery daemon over its localhost HTTP + WS
// API. It is the production Client; the TUI model and status renderer only see
// the Client interface, so this type is exercised via the live-daemon smoke and
// the interface stub rather than unit tests (its logic is thin request plumbing).
type HTTPClient struct {
	base string // e.g. http://127.0.0.1:7777 (no trailing slash)
	hc   *http.Client
}

// NewHTTPClient builds a client for the daemon at baseURL. A short default
// timeout keeps `swarmery status` snappy when the daemon is down. Every
// request is signed with the link token in ~/.swarmery/link-token
// (internal/linksig): the daemon takes a write or a WS upgrade that carries no
// Origin only with link auth.
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		base: strings.TrimRight(baseURL, "/"),
		hc:   linksig.NewClient("", 4*time.Second),
	}
}

func (c *HTTPClient) BaseURL() string { return c.base }

// Snapshot fetches health + today-stats + pending approvals. It is resilient:
// if health fails the daemon is treated as unreachable (Reachable=false, err
// set) so `swarmery status` can print the down banner and exit nonzero; if only
// the softer endpoints fail the block still renders with what came back.
func (c *HTTPClient) Snapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	if err := c.getJSON(ctx, "/api/health", &snap.Health); err != nil {
		return snap, fmt.Errorf("daemon unreachable: %w", err)
	}
	snap.Reachable = true
	// Soft endpoints: a failure degrades the block but does not fail status.
	_ = c.getJSON(ctx, "/api/stats/today", &snap.Stats)
	_ = c.getJSON(ctx, "/api/approvals", &snap.Approvals)
	return snap, nil
}

// Logs fetches ring entries newer than sinceID.
func (c *HTTPClient) Logs(ctx context.Context, sinceID int64, tag string, limit int) ([]LogEntry, int64, error) {
	path := "/api/logs?sinceId=" + strconv.FormatInt(sinceID, 10)
	if tag != "" {
		path += "&tag=" + tag
	}
	if limit > 0 {
		path += "&limit=" + strconv.Itoa(limit)
	}
	var resp logsResponse
	if err := c.getJSON(ctx, path, &resp); err != nil {
		return nil, sinceID, err
	}
	return resp.Entries, resp.LastID, nil
}

// ResolveApproval POSTs the dashboard decision for a pending request.
func (c *HTTPClient) ResolveApproval(ctx context.Context, id int64, action string) error {
	body, _ := json.Marshal(map[string]string{"action": action})
	return c.postJSON(ctx, "/api/approvals/"+strconv.FormatInt(id, 10), body)
}

// PauseDispatch flips the global dispatcher pause flag.
func (c *HTTPClient) PauseDispatch(ctx context.Context, paused bool) error {
	body, _ := json.Marshal(map[string]any{"scope": "global", "paused": paused})
	return c.postJSON(ctx, "/api/dispatch/pause", body)
}

// DispatchStatus reads GET /api/dispatch (used to enrich the status block's slot
// total). Satisfies the optional dispatchReader capability. 503 (dispatcher not
// attached) surfaces as an error the caller treats as "unknown slots".
func (c *HTTPClient) DispatchStatus(ctx context.Context) (Dispatch, error) {
	var d Dispatch
	err := c.getJSON(ctx, "/api/dispatch", &d)
	return d, err
}

// StreamEvents dials /api/ws and delivers decoded envelopes on out until ctx is
// cancelled or the socket drops (whichever first). It returns the terminating
// error so the caller's reconnect loop can back off. No new message type is sent
// or expected — the daemon streams the frozen envelope and we never write.
func (c *HTTPClient) StreamEvents(ctx context.Context, out chan<- WSEvent) error {
	wsURL := "ws" + strings.TrimPrefix(c.base, "http") + "/api/ws"
	u, err := url.Parse(wsURL)
	if err != nil {
		return err
	}
	// The upgrade is signed like a write; a fresh signature per dial, so a
	// reconnect never replays a spent nonce.
	hdr, err := linksig.Header("", http.MethodGet, u.RequestURI(), nil)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var evt WSEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			continue // skip a malformed frame, keep the stream alive
		}
		select {
		case out <- evt:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *HTTPClient) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *HTTPClient) postJSON(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}
