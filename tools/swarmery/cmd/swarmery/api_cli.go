// `swarmery api <METHOD> <path> [--data …]` — one request to the daemon's HTTP
// API, signed with the link token (internal/linksig). Non-browser clients
// authenticate to the daemon: a write that carries no Origin needs link auth
// (internal/api origin.go), and this is how a shell script, a hook or a
// routine step gets it without reimplementing HMAC in bash.
//
// Exit codes are the contract: 0 the daemon answered 2xx, 1 it answered any
// other status (the body still goes to stdout), 2 a usage error or no answer
// at all (daemon down, timeout).
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

const apiUsage = "usage: swarmery api <METHOD> <path> [--data <json>|@file|@-] [--port <n>] [--url <base>] [--key-file <path>] [--timeout <d>]"

// maxAPIAnswer bounds the answer copied to stdout.
const maxAPIAnswer = 64 << 20

func cmdAPI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usageErr := func(format string, a ...any) int {
		if format != "" {
			fmt.Fprintf(stderr, "swarmery api: "+format+"\n", a...)
		}
		fmt.Fprintln(stderr, apiUsage)
		return 2
	}

	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", envPort(), "daemon port (env: SWARMERY_PORT)")
	baseURL := fs.String("url", "", "daemon base URL (default http://127.0.0.1:<port>)")
	data := fs.String("data", "", "request body: literal JSON, @file, or @- for stdin")
	keyFile := fs.String("key-file", "", "link token file (default ~/.swarmery/link-token)")
	timeout := fs.Duration("timeout", 30*time.Second, "overall request timeout")
	// The documented form puts METHOD and path first and flags after them;
	// flags before them work too. flag stops at the first positional, so
	// parse, peel the positionals off the front, and parse what follows.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return usageErr("%v", err)
		}
		args = fs.Args()
		for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			pos, args = append(pos, args[0]), args[1:]
		}
		if len(args) == 0 {
			break
		}
	}
	if len(pos) != 2 {
		return usageErr("want METHOD and path")
	}
	method, path := strings.ToUpper(pos[0]), pos[1]
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return usageErr("unsupported method %q", pos[0])
	}
	// A path, never a URL: the signature covers the request target, so the
	// host comes only from --port/--url.
	if !strings.HasPrefix(path, "/") {
		return usageErr("path must start with / (got %q)", path)
	}
	if *timeout <= 0 {
		return usageErr("--timeout must be positive")
	}

	body, err := apiBody(*data, stdin)
	if err != nil {
		return usageErr("--data: %v", err)
	}
	base := strings.TrimRight(*baseURL, "/")
	if base == "" {
		base = fmt.Sprintf("http://127.0.0.1:%d", *port)
	}
	kp := *keyFile
	if kp == "" {
		kp = linksig.DefaultKeyPath()
	}
	if linksig.ReadKeyFile(kp) == "" {
		fmt.Fprintf(stderr, "swarmery api: no link token at %s; the request goes out unsigned (run `swarmery link token` to mint one)\n", kp)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return usageErr("%v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := linksig.NewClient(kp, 0).Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "swarmery api: daemon unreachable at %s: %v\n", base, err)
		return 2
	}
	defer resp.Body.Close()
	if _, err := io.Copy(stdout, io.LimitReader(resp.Body, maxAPIAnswer)); err != nil {
		fmt.Fprintf(stderr, "swarmery api: read answer: %v\n", err)
		return 2
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(stderr, "swarmery api: %s %s: %s\n", method, path, resp.Status)
		return 1
	}
	return 0
}

// apiBody resolves --data: "" is no body, "@-" reads stdin, "@<file>" reads
// the file, anything else is the body itself.
func apiBody(data string, stdin io.Reader) ([]byte, error) {
	switch {
	case data == "":
		return nil, nil
	case data == "@-":
		return io.ReadAll(io.LimitReader(stdin, linksig.MaxBody+1))
	case strings.HasPrefix(data, "@"):
		return os.ReadFile(data[1:])
	default:
		return []byte(data), nil
	}
}
