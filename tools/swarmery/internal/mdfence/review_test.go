package mdfence

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseReviewsFixtures(t *testing.T) {
	cases := []struct {
		file     string
		rounds   []string // rounds of the parsed blocks, in document order
		verdicts []string
		nErrs    int
		latest   string // Latest(...).Round; "" = nil
	}{
		{file: "pass-with-block.md", rounds: []string{"1"}, verdicts: []string{"PASS"}, latest: "1"},
		{file: "pass-no-block.md"},
		{file: "fail-round1.md", rounds: []string{"1"}, verdicts: []string{"FAIL"}, latest: "1"},
		{file: "round2-resolves.md", rounds: []string{"2", "1"}, verdicts: []string{"PASS", "FAIL"}, latest: "2"},
		{file: "incomplete.md", rounds: []string{"1"}, verdicts: []string{"INCONCLUSIVE"}, latest: "1"},
		{file: "unparsable.md", rounds: []string{"3"}, verdicts: []string{"FAIL"}, nErrs: 2, latest: "3"},
		{file: "plan-round.md", rounds: []string{"1", "plan", "2"}, verdicts: []string{"FAIL", "PASS", "PASS"}, latest: "plan"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			rs, errs := ParseReviews(fixture(t, "review-blocks", c.file))
			if len(errs) != c.nErrs {
				t.Fatalf("errors = %+v, want %d", errs, c.nErrs)
			}
			var rounds, verdicts []string
			for _, r := range rs {
				rounds = append(rounds, r.Round)
				verdicts = append(verdicts, r.Verdict)
			}
			if !reflect.DeepEqual(rounds, c.rounds) {
				t.Errorf("rounds = %q, want %q", rounds, c.rounds)
			}
			if !reflect.DeepEqual(verdicts, c.verdicts) {
				t.Errorf("verdicts = %q, want %q", verdicts, c.verdicts)
			}
			got := ""
			if l := Latest(rs); l != nil {
				got = l.Round
			}
			if got != c.latest {
				t.Errorf("Latest round = %q, want %q", got, c.latest)
			}
		})
	}
}

func TestParseReviewsFields(t *testing.T) {
	t.Run("pass-with-block", func(t *testing.T) {
		rs, _ := ParseReviews(fixture(t, "review-blocks", "pass-with-block.md"))
		r := rs[0]
		if !r.Complete || len(r.Findings) != 0 {
			t.Errorf("review = %+v, want complete with no findings", r)
		}
		if !reflect.DeepEqual(r.IDs, []IDEvidence{{ID: "ExportJob", Evidence: "src/lib/export/job.ts:12"}}) {
			t.Errorf("ids = %+v", r.IDs)
		}
		if !reflect.DeepEqual(r.CheckedCallers, []string{"src/app/api/export/route.ts:30"}) {
			t.Errorf("checked_callers = %q", r.CheckedCallers)
		}
		if r.Line != 11 {
			t.Errorf("line = %d, want 11 (the opening fence)", r.Line)
		}
	})

	t.Run("fail-round1", func(t *testing.T) {
		rs, _ := ParseReviews(fixture(t, "review-blocks", "fail-round1.md"))
		r := rs[0]
		if len(r.Findings) != 2 {
			t.Fatalf("findings = %+v, want 2", r.Findings)
		}
		f := r.Findings[0]
		want := Finding{ID: "R1-1", Type: "silent-failure", File: "src/lib/export/writer.ts", Line: 84,
			Criterion: "upload failure is reported", Evidence: "catch {} swallows the putObject error and the job is marked done"}
		note := f.Note
		f.Note = ""
		if !reflect.DeepEqual(f, want) {
			t.Errorf("finding = %+v\nwant      %+v", f, want)
		}
		if !strings.Contains(note, "still returns 200") {
			t.Errorf("multi-line note lost: %q", note)
		}
		if !r.Findings[1].TestCaught || r.Findings[1].Type != "contract" {
			t.Errorf("second finding = %+v, want a test-caught contract finding", r.Findings[1])
		}
		if !reflect.DeepEqual(r.Drift, []Drift{{Criterion: "upload failure is reported",
			File: "src/lib/export/writer.ts", Note: "ticked, but the failure is swallowed"}}) {
			t.Errorf("drift = %+v", r.Drift)
		}
	})

	t.Run("round2-resolves", func(t *testing.T) {
		rs, _ := ParseReviews(fixture(t, "review-blocks", "round2-resolves.md"))
		if !reflect.DeepEqual(Latest(rs).Resolves, []string{"R1-1", "R1-2"}) {
			t.Errorf("resolves = %q", Latest(rs).Resolves)
		}
	})

	t.Run("incomplete", func(t *testing.T) {
		rs, _ := ParseReviews(fixture(t, "review-blocks", "incomplete.md"))
		r := rs[0]
		if r.Complete {
			t.Error("complete = true, want false")
		}
		if !reflect.DeepEqual(r.ScopeLeft, []string{"internal/store/**/*", "web/src/pages/Plans.tsx"}) {
			t.Errorf("scope_left = %q", r.ScopeLeft)
		}
	})

	t.Run("unparsable reasons and lines", func(t *testing.T) {
		_, errs := ParseReviews(fixture(t, "review-blocks", "unparsable.md"))
		if len(errs) != 2 {
			t.Fatalf("errors = %+v", errs)
		}
		if errs[0].Kind != ErrUnparsable || !strings.Contains(errs[0].Reason, "missing verdict") || errs[0].Line != 7 {
			t.Errorf("first error = %+v, want unparsable 'missing verdict' at line 7", errs[0])
		}
		if !strings.Contains(errs[1].Reason, "findings") || errs[1].Line != 18 {
			t.Errorf("second error = %+v, want a findings error at line 18", errs[1])
		}
	})
}

func TestParseReviewsShapes(t *testing.T) {
	cases := []struct {
		name, body string
		wantErr    string // "" = parses
	}{
		{"missing round", "kind: review\nverdict: PASS", "missing round"},
		{"round zero", "kind: review\nround: 0\nverdict: PASS", "positive number"},
		{"round word", "kind: review\nround: final\nverdict: PASS", "positive number"},
		{"unknown verdict", "kind: review\nround: 1\nverdict: MAYBE", "not PASS, FAIL or INCONCLUSIVE"},
		{"complete not bool", "kind: review\nround: 1\nverdict: PASS\ncomplete: sometimes", "complete"},
		{"line not a number", "kind: review\nround: 1\nverdict: FAIL\nfindings: [{id: a, line: top}]", "line"},
		{"findings a map", "kind: review\nround: 1\nverdict: FAIL\nfindings: {id: a}", "list of maps"},
		{"lowercase verdict and unknown keys", "kind: review\nround: 1\nverdict: pass\nreviewer: opus", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs, errs := ParseReviews("```yaml\n" + c.body + "\n```\n")
			if c.wantErr == "" {
				if len(errs) != 0 || len(rs) != 1 {
					t.Fatalf("reviews = %+v, errors = %+v; want one review", rs, errs)
				}
				if rs[0].Verdict != ReviewPass {
					t.Errorf("verdict = %q, want normalized PASS", rs[0].Verdict)
				}
				return
			}
			if len(errs) != 1 || !strings.Contains(errs[0].Reason, c.wantErr) {
				t.Fatalf("errors = %+v, want one containing %q", errs, c.wantErr)
			}
		})
	}
}

func TestLatest(t *testing.T) {
	if Latest(nil) != nil {
		t.Error("Latest(nil) != nil")
	}
	rs := []Review{{Round: "1"}, {Round: "10"}, {Round: "2"}}
	if got := Latest(rs).Round; got != "10" {
		t.Errorf("Latest = %q, want numeric order (10)", got)
	}
	rs = []Review{{Round: "plan", Verdict: "FAIL"}, {Round: "2"}, {Round: "plan", Verdict: "PASS"}}
	if got := Latest(rs); got.Round != "plan" || got.Verdict != "PASS" {
		t.Errorf("Latest = %+v, want the later plan block", got)
	}
}
