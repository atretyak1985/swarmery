package api

// Tests for the ACCOUNT-scoped terminal (term.go, ?account=<key>) — the
// connect flow's interactive login fallback. The gates under test all run
// BEFORE the websocket upgrade: a rejected request is a plain non-101 status,
// surfaced by websocket.Dial as an error.
//
// Isolation matches term_account_test.go: unsetConfigDir + attachHomeAccounts,
// so neither the developer's shell nor their real ~/.claude* skews resolution.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

func termAccountWSURL(srv *httptest.Server, account string) string {
	base := strings.Replace(srv.URL, "http://", "ws://", 1)
	return base + "/api/term/ws?account=" + url.QueryEscape(account)
}

// TestResolveTermAccount: the env is claudeacct.SpawnEnv over the daemon's own
// (exactly one CLAUDE_CONFIG_DIR for a named account, NONE for the default —
// absence selects it), the cwd is the operator's home, and a malformed or
// unknown key is refused before anything resolves.
func TestResolveTermAccount(t *testing.T) {
	unsetConfigDir(t)
	home, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")

	cwd, env, ok := resolveTermAccount("nabu-org")
	if !ok {
		t.Fatal("resolveTermAccount(nabu-org) = not ok, want ok")
	}
	if cwd != home {
		t.Errorf("cwd = %q, want the operator's home %q", cwd, home)
	}
	if want := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"]}; !slices.Equal(termConfigDirs(env), want) {
		t.Errorf("CLAUDE_CONFIG_DIR entries = %v, want %v", termConfigDirs(env), want)
	}

	cwd, env, ok = resolveTermAccount(ingest.DefaultAccount)
	if !ok || cwd != home {
		t.Fatalf("resolveTermAccount(default) = (%q, ok=%v), want home and ok", cwd, ok)
	}
	if got := termConfigDirs(env); len(got) != 0 {
		t.Errorf("default account env carries %v, want none — absence of CLAUDE_CONFIG_DIR selects the default", got)
	}

	for _, key := range []string{"../evil", "a b", ".", ""} {
		if _, _, ok := resolveTermAccount(key); ok {
			t.Errorf("resolveTermAccount(%q) = ok, want refused — the key must never become a path", key)
		}
	}
	// Well-formed but absent from the registry: refused too. EnvForAccount
	// alone would fall back to the canonical dir for it; the registry check is
	// what stops that fallback from minting terminals for accounts that do not
	// exist.
	if _, _, ok := resolveTermAccount("ghost"); ok {
		t.Error("resolveTermAccount(ghost) = ok, want refused — the account does not exist")
	}
}

// TestTermAccountQueryGates is the HTTP matrix: a known account upgrades (and
// the PTY is live — the fake shell echoes); an unknown account, a malformed
// key, account+cwd together, and a missing Origin are all rejected before the
// upgrade.
func TestTermAccountQueryGates(t *testing.T) {
	unsetConfigDir(t)
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	srv, projectDir, _ := termTestServer(t)

	t.Run("accept/known account", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, termAccountWSURL(srv, "nabu-org"), &websocket.DialOptions{
			HTTPHeader: localOrigin(srv),
		})
		if err != nil {
			t.Fatalf("dial should have upgraded for a known account: %v", err)
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		if err := c.Write(ctx, websocket.MessageBinary, []byte("ping\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if typ != websocket.MessageBinary || !strings.Contains(string(data), "ping") {
			t.Errorf("echo = (%v, %q), want a binary 'ping'", typ, data)
		}
	})

	reject := []struct {
		name string
		url  string
	}{
		{"unknown account", termAccountWSURL(srv, "ghost")},
		{"malformed key", termAccountWSURL(srv, "../evil")},
		{"account and cwd together", termAccountWSURL(srv, "nabu-org") + "&cwd=" + url.QueryEscape(projectDir)},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, tc.url, &websocket.DialOptions{
				HTTPHeader: localOrigin(srv),
			})
			if err == nil {
				c.Close(websocket.StatusNormalClosure, "")
				t.Fatal("dial upgraded, want a pre-upgrade rejection")
			}
		})
	}

	t.Run("reject/origin-less", func(t *testing.T) {
		// The strict origin gate holds on the account form too: a raw dial with
		// no Origin at all must be rejected exactly like the cwd form.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, termAccountWSURL(srv, "nabu-org"), &websocket.DialOptions{
			HTTPHeader: http.Header{},
		})
		if err == nil {
			c.Close(websocket.StatusNormalClosure, "")
			t.Fatal("dial upgraded without an Origin, want 403")
		}
	})
}
