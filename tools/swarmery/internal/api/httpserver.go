package api

import (
	"net/http"
	"time"
)

// Connection timeouts of the daemon's HTTP server. They bound how long a client
// may take to SEND a request and how long an idle keep-alive connection is
// kept, so a client that opens connections and stalls cannot pin them forever.
//
// There is deliberately no WriteTimeout: it would bound how long a handler may
// take to ANSWER, and several answer for minutes by design — the approvals
// long-poll (POST /api/hooks/permission-request, up to the approval timeout),
// the event bus (/api/ws) and the terminal (/api/term/ws), which are
// websockets. ReadTimeout does not reach them: net/http clears the read
// deadline once the request body has been read, and again when a websocket
// hijacks the connection (httpserver_test.go pins both).
const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 60 * time.Second
	serverIdleTimeout       = 120 * time.Second
)

// NewHTTPServer is the daemon's http.Server for addr and handler, with the
// connection timeouts above.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return newHTTPServer(addr, handler, serverReadHeaderTimeout, serverReadTimeout, serverIdleTimeout)
}

// newHTTPServer is NewHTTPServer with the timeouts as parameters, so tests can
// exercise them in milliseconds.
func newHTTPServer(addr string, handler http.Handler, readHeader, read, idle time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeader,
		ReadTimeout:       read,
		IdleTimeout:       idle,
	}
}
