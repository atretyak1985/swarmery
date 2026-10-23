package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// The count refuses the default store even through a symlink, and counts a
// copy read-only without printing a value.
func TestRescrubCountRefusesDefaultStoreAndCountsACopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def, err := store.DefaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(def)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	link := filepath.Join(t.TempDir(), "looks-like-a-copy.db")
	if err := os.Symlink(def, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{def, link} {
		err := cmdRescrub([]string{"--count", "--db", p}, &bytes.Buffer{})
		if !isUsage(err) {
			t.Errorf("--db %s: err = %v, want the default-store refusal", p, err)
		}
	}

	copyPath := filepath.Join(t.TempDir(), "copy.db")
	cp, err := store.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redacttest.Seed(cp, 200); err != nil {
		t.Fatal(err)
	}
	cp.Close()
	var out bytes.Buffer
	if err := cmdRescrub([]string{"--count", "--db", copyPath}, &out); err != nil {
		t.Fatalf("count: %v", err)
	}
	if !strings.Contains(out.String(), "total ") || strings.Contains(out.String(), "total 0 ") {
		t.Errorf("count output has no non-zero total:\n%s", out.String())
	}
	for _, n := range redacttest.Needles() {
		if strings.Contains(out.String(), n) {
			t.Fatal("count output holds a secret value")
		}
	}
}
