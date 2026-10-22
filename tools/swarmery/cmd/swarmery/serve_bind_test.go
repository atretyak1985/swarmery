package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
)

// TestServeRefusesNonLoopbackBindWithoutLinkToken: a daemon reachable beyond
// this machine must not start until non-browser clients have a credential to
// authenticate with — and serve must not mint one on its own for that case.
func TestServeRefusesNonLoopbackBindWithoutLinkToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	errc := make(chan error, 1)
	go func() {
		errc <- cmdServe([]string{"--bind", "0.0.0.0", "--port", "0", "--no-ingest",
			"--db", filepath.Join(home, "serve.db")})
	}()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "link token") {
			t.Fatalf("cmdServe(--bind 0.0.0.0) without a token = %v, want a refusal naming the link token", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cmdServe(--bind 0.0.0.0) started serving without a link token")
	}
	if _, err := os.Stat(link.TokenPath(home)); err == nil {
		t.Error("the refused serve minted a link token")
	}
}
