package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// A session whose model changes in a LATER ingest batch — a resumed session
// (`claude -r … --model …`) or any mid-file tail — must still read as changed.
// Ingest overwrites sessions.model with each batch's model, so the opening
// model has to come from turns, not from that column (found by plan 546's
// step-8.2 smoke test: sonnet-5 → haiku-4-5 reported modelChanged=false).
func TestSessionModelChangeAcrossIngestBatches(t *testing.T) {
	srv, db := testServerWithDB(t)
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "model-fallback-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimRight(string(src), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("fixture has %d lines, want 5", len(lines))
	}
	path := filepath.Join(t.TempDir(), "fb00fb00-0000-4000-8000-00000000fb01.jsonl")
	// Batch 1: the two opus-5-5 turns. Batch 2: the fallback record and the
	// opus-4-1 turn, tailed later.
	if err := os.WriteFile(path, []byte(strings.Join(lines[:3], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	// TailFile, not File: the daemon tails from the stored byte offset, so the
	// second batch starts at the fallback — File re-reads from byte 0 and would
	// hide the bug.
	if _, err := ingest.TailFile(db, path, "", ingest.DefaultThresholds()); err != nil {
		t.Fatalf("ingest batch 1: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines[3:], "") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ingest.TailFile(db, path, "", ingest.DefaultThresholds()); err != nil {
		t.Fatalf("ingest batch 2: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/fb00fb00-0000-4000-8000-00000000fb01")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s struct {
		ModelLast     *string `json:"modelLast"`
		ModelChanged  bool    `json:"modelChanged"`
		ModelFellBack bool    `json:"modelFellBack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.ModelLast == nil || *s.ModelLast != "claude-opus-4-1" {
		t.Errorf("modelLast = %v, want claude-opus-4-1", s.ModelLast)
	}
	if !s.ModelChanged || !s.ModelFellBack {
		t.Errorf("modelChanged=%v modelFellBack=%v, want both true (opus-5-5 → opus-4-1)", s.ModelChanged, s.ModelFellBack)
	}
}
