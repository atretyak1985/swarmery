package workaround

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDir = "../../testdata/fixtures/workaround"

// fixtureTurn is one line of phase4-landing.jsonl: an assistant text, or a
// SubagentHandback report, of the reference plan's phase 4 executors.
type fixtureTurn struct {
	Turn     int    `json:"turn"`
	Agent    string `json:"agent"`
	Dispatch string `json:"dispatch"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

// expectedHit is a golden hit with a human verdict: "true" = the agent really
// changed course or excused something, "false" = the word is used for
// something else (a false positive).
type expectedHit struct {
	Turn     int    `json:"turn"`
	Word     string `json:"word"`
	Sentence string `json:"sentence"`
	Verdict  string `json:"verdict"`
	Why      string `json:"why"`
}

type expectedFile struct {
	Source string `json:"source"`
	// MustFind is the hit the plan names: phase 4's worktree case.
	MustFind struct {
		Word             string `json:"word"`
		SentenceContains string `json:"sentenceContains"`
		ContextContains  string `json:"contextContains"`
	} `json:"mustFind"`
	FalsePositiveBudget int           `json:"falsePositiveBudget"`
	Hits                []expectedHit `json:"hits"`
}

func loadFixture(t *testing.T) ([]fixtureTurn, expectedFile) {
	t.Helper()
	f, err := os.Open(filepath.Join(fixtureDir, "phase4-landing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var turns []fixtureTurn
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var ft fixtureTurn
		if err := json.Unmarshal(sc.Bytes(), &ft); err != nil {
			t.Fatal(err)
		}
		turns = append(turns, ft)
	}
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exp expectedFile
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	return turns, exp
}

// The reference plan's phase 4 (phase-landing-repo-provider): the executor's
// own words about the warm-branch / worktree reclaim are found, every hit is
// labelled in expected.json, and the false positives stay within budget.
func TestFixturePhase4Landing(t *testing.T) {
	turns, exp := loadFixture(t)
	m := Compile(d23(t))

	type key struct {
		turn           int
		word, sentence string
	}
	var got []Hit
	gotKeys := map[key]bool{}
	for _, ft := range turns {
		for _, h := range m.Scan(ft.Text) {
			h.TurnSeq = ft.Turn
			got = append(got, h)
			gotKeys[key{ft.Turn, h.Word, h.Sentence}] = true
		}
	}

	found := false
	for _, h := range got {
		ctx := h.Before + " " + h.Sentence + " " + h.After
		if h.Word == exp.MustFind.Word && strings.Contains(h.Sentence, exp.MustFind.SentenceContains) &&
			strings.Contains(ctx, exp.MustFind.ContextContains) {
			found = true
		}
	}
	if !found {
		t.Errorf("the worktree hit (%+v) is missing", exp.MustFind)
	}

	// The golden list is exhaustive: every hit is labelled, every label is hit.
	wantKeys := map[key]bool{}
	falsePos := 0
	for _, e := range exp.Hits {
		k := key{e.Turn, e.Word, e.Sentence}
		wantKeys[k] = true
		if !gotKeys[k] {
			t.Errorf("expected hit not found: %+v", e)
		}
		switch e.Verdict {
		case "false":
			falsePos++
		case "true":
		default:
			t.Errorf("hit %+v has verdict %q, want true|false", e, e.Verdict)
		}
	}
	for _, h := range got {
		if !wantKeys[key{h.TurnSeq, h.Word, h.Sentence}] {
			b, _ := json.Marshal(h)
			t.Errorf("unlabelled hit — add it to expected.json with a verdict: %s", b)
		}
	}
	if falsePos > exp.FalsePositiveBudget {
		t.Errorf("%d false positives, budget %d", falsePos, exp.FalsePositiveBudget)
	}
	t.Logf("phase 4: %d hits, %d false positives", len(got), falsePos)
}
