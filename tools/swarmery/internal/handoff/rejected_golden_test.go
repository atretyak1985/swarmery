package handoff

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite the recorded handoff goldens")

// checkGolden compares got with testdata/<name>, rewriting it under -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s drifted (run `go test ./internal/handoff -run %s -update` if intended)\n--- got ---\n%s\n--- want ---\n%s",
			name, t.Name(), got, want)
	}
}

// rejectedModelOut is a model reply whose Rejected section overflows the cap:
// a reworded copy of one structured line, a verbatim copy of another, the
// placeholder, and nine paths taken from the turn text.
const rejectedModelOut = `# Handoff: ship the cache layer
## State — what is done and verified vs in progress
- store chosen, eviction pending
## Non-goals — what this session deliberately did not do or ruled out
- no cache metrics
## Rejected — options already ruled out
- Redis — chose SQLite table instead [decision D-1]
- Redis as the store — dropped
- none recorded
- memcached — another service to run
- write-through on every turn — doubled the write load
- per-project cache files — lost on prune
- in-process map — not shared across the API and the scanner
- cron-based refresh — hides staleness
- caching the transcript — the idea boundary forbids it
- a second SQLite DB — two migration paths
- JSON blobs in sessions — unindexable
- bloom filter front — premature
## Open risks & failed checks
- unknown
## Next step
- implement TTL in internal/cache
## Verification
- go test ./internal/cache/...
`

// The prompt the generator hands the model for a plan session: the template
// asks for the Rejected section after Non-goals, and the digest carries the
// structured lines from all three sources.
func TestPromptRejectedGolden(t *testing.T) {
	db := seededDB(t)
	seedUserTurn(t, db, 1, 1, 50*time.Minute, "Add a cache layer; Redis is off the table.")
	seedAssistantTurn(t, db, 1, 2, Threshold+5_000, 40*time.Minute, "Tried an in-process map first; dropped it because the scanner runs in another goroutine pool.")

	fr := &fakeRunner{out: rejectedModelOut}
	if _, err := generateInto(db, fr, 1, testNow, t.TempDir()); err != nil {
		t.Fatalf("generateInto: %v", err)
	}
	ng := strings.Index(fr.gotPrompt, "\n## Non-goals")
	rj := strings.Index(fr.gotPrompt, "\n## Rejected —")
	or := strings.Index(fr.gotPrompt, "\n## Open risks")
	if !(ng >= 0 && ng < rj && rj < or) {
		t.Errorf("template must ask for Rejected after Non-goals and before Open risks (at %d, %d, %d)", ng, rj, or)
	}
	checkGolden(t, "prompt-rejected.golden", fr.gotPrompt)
}

// The written brief: structured lines first and verbatim, the model's copies
// and placeholder dropped, its own additions kept, the body capped at 10 lines
// with the overflow counted.
func TestBriefRejectedCapGolden(t *testing.T) {
	db := seededDB(t)
	fr := &fakeRunner{out: rejectedModelOut}
	path, err := generateInto(db, fr, 1, testNow, t.TempDir())
	if err != nil {
		t.Fatalf("generateInto: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	body := string(raw)

	ls := strings.Split(body, "\n")
	start := headingIndex(ls, rejectedHeading)
	if start < 0 || ls[start] != rejectedHeading {
		t.Fatalf("brief must carry the canonical %q heading; body=%s", rejectedHeading, body)
	}
	var section []string
	for _, l := range ls[start+1 : sectionEnd(ls, start)] {
		if strings.TrimSpace(l) != "" {
			section = append(section, l)
		}
	}
	if len(section) != maxRejectedLines {
		t.Errorf("section has %d lines, want the cap %d:\n%s", len(section), maxRejectedLines, strings.Join(section, "\n"))
	}
	// 6 structured + 10 distinct model additions = 16; 9 kept, 7 counted.
	if last := section[len(section)-1]; last != "- … and 7 more" {
		t.Errorf("last line = %q, want the overflow marker", last)
	}
	if strings.Count(body, "- Redis — chose SQLite table instead [decision D-1]") != 1 {
		t.Errorf("a verbatim copy of a structured line must not repeat")
	}
	if strings.Contains(body, "none recorded") {
		t.Errorf("placeholder must go once real lines are listed")
	}
	if !(headingIndex(ls, nonGoalsHeading) < start && start < headingIndex(ls, openRisksHeading)) {
		t.Errorf("Rejected must sit between Non-goals and Open risks")
	}
	checkGolden(t, "brief-rejected-cap.golden", body)
}

// The model dropped the section: it is inserted after Non-goals with the
// structured lines.
func TestFitRejectedInsertsMissingSection(t *testing.T) {
	out := "# Handoff: x\n## Non-goals\n- a\n\n## Open risks & failed checks\n- b\n"
	got := fitRejected(out, []Rejected{{Option: "Redis", Why: "chose SQLite instead", Source: "decision D-1"}})
	want := "# Handoff: x\n## Non-goals\n- a\n\n## Rejected\n- Redis — chose SQLite instead [decision D-1]\n\n## Open risks & failed checks\n- b\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Nothing structured and nothing from the model: the placeholder stands, and
// a brief that already says so is left as written.
func TestFitRejectedNoneRecorded(t *testing.T) {
	out := "# Handoff: x\n## Non-goals\n- a\n\n## Rejected — none\n- none recorded\n\n## Next step\n- go\n"
	want := "# Handoff: x\n## Non-goals\n- a\n\n## Rejected\n- none recorded\n\n## Next step\n- go\n"
	if got := fitRejected(out, nil); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
