package api

// The daemon's http.Server bounds how long a client may take to SEND a request
// (headers, then body) and how long an idle keep-alive connection lingers, but
// never how long a handler may take to answer: the approvals long-poll holds a
// request for minutes, and the event bus and the terminal are websockets.
// These tests run the real constructor with shrunken timeouts.

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestNewHTTPServerSetsTimeouts(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 60*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Errorf("timeouts = header %s, read %s, idle %s; want 10s, 60s, 120s",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %s, want 0: websockets and long-polls write for as long as they live", srv.WriteTimeout)
	}
}

// serveTest starts newHTTPServer on a loopback listener and returns its address.
func serveTest(t *testing.T, h http.Handler, header, read, idle time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", h, header, read, idle)
	go srv.Serve(ln) //nolint:errcheck // closed by Cleanup
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// A handler that has read its whole request may wait far past ReadTimeout:
// the read deadline covers receiving the request, not answering it. net/http
// clears it when it starts watching the connection for a client hang-up; if
// it ever stopped doing so, the expiring deadline would cancel the request
// context mid-wait and every long-poll would be cut off at ReadTimeout.
func TestReadTimeoutDoesNotCutLongPolls(t *testing.T) {
	const read = 300 * time.Millisecond
	wait := 4 * read
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		select {
		case <-time.After(wait):
			io.WriteString(w, "answered") //nolint:errcheck
		case <-r.Context().Done():
			// The client sees an empty reply; the test reports it.
		}
	})
	addr := serveTest(t, handler, read, read, time.Minute)

	for _, tc := range []struct{ method, body string }{
		{"POST", `{"session":"x"}`}, // body read to EOF, like the approvals long-poll
		{"GET", ""},                 // no body at all, like the link long-polls
	} {
		req, _ := http.NewRequest(tc.method, "http://"+addr+"/wait", strings.NewReader(tc.body))
		if tc.body == "" {
			req.Body = http.NoBody
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s long-poll: %v", tc.method, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(raw) != "answered" {
			t.Errorf("%s long-poll past ReadTimeout got %q, want the answer", tc.method, raw)
		}
	}
}

// A client that never finishes its headers is dropped after ReadHeaderTimeout.
func TestSlowHeadersAreDropped(t *testing.T) {
	const header = 300 * time.Millisecond
	addr := serveTest(t, http.NotFoundHandler(), header, time.Minute, time.Minute)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nX-Slow: ")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	start := time.Now()
	// A 408 may precede the close; drain until the server hangs up.
	_, err = io.Copy(io.Discard, conn)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection still open after %s: the header timeout did not fire", time.Since(start))
	}
}

// A slow body is still bounded by ReadTimeout: the deadline is cleared only
// once the body has been read to its end.
func TestSlowBodyIsCutAtReadTimeout(t *testing.T) {
	const read = 300 * time.Millisecond
	got := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		got <- err
	})
	addr := serveTest(t, handler, read, read, time.Minute)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n{")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Error("a body that never arrives must fail its read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow body was never cut off")
	}
}

// A websocket idles far past ReadTimeout and still carries a message.
func TestWebsocketSurvivesIdlePastReadTimeout(t *testing.T) {
	const read = 300 * time.Millisecond
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow() //nolint:errcheck
		typ, msg, err := c.Read(context.Background())
		if err != nil {
			return
		}
		c.Write(context.Background(), typ, msg) //nolint:errcheck
	})
	addr := serveTest(t, handler, read, read, read)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()   //nolint:errcheck
	time.Sleep(5 * read) // idle past ReadTimeout and IdleTimeout
	if err := c.Write(ctx, websocket.MessageText, []byte("still-here")); err != nil {
		t.Fatalf("write after idle: %v", err)
	}
	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read after idle: %v", err)
	}
	if string(msg) != "still-here" {
		t.Errorf("echo = %q", msg)
	}
}
