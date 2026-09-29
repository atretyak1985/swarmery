package modeleval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadBench(t *testing.T, path string) BenchRun {
	t.Helper()
	run, err := LoadBench(path)
	if err != nil {
		t.Fatalf("LoadBench(%s): %v", path, err)
	}
	return run
}

func TestCompareBench(t *testing.T) {
	base := loadBench(t, filepath.Join("testdata", "bench-base.json")) // 3/5
	cases := []struct {
		file       string
		verdict    string
		cand, n    int
		detailWant []string
	}{
		{"bench-cand-worse.json", VerdictFail, 2, 5, []string{
			"bench 2/5 vs 3/5", "turns 34 vs 36", "cost $1.19 vs $1.81",
			"candidate-model/medium vs baseline-model/high",
			"tasks without a result counted as 0 turns", // b05 timed out with null numbers
		}},
		{"bench-cand-equal.json", VerdictPass, 3, 5, []string{"bench 3/5 vs 3/5"}},
		{"bench-cand-better.json", VerdictPass, 4, 5, []string{"bench 4/5 vs 3/5"}},
		{"bench-mismatch.json", VerdictInconclusive, 5, 5, []string{
			"not comparable: task sets differ",
			"candidate-only [b06-extra]", "baseline-only [b05-read-the-docs]",
			"bench 5/5 vs 3/5",
		}},
		// 3/5 vs 3/5 would pass on the numbers alone; a broken run is not a
		// model result, so the comparison must refuse instead.
		{"bench-cand-error.json", VerdictInconclusive, 3, 5, []string{
			"not comparable: run error in b04-stale-premise", "bench 3/5 vs 3/5",
		}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			cand := loadBench(t, filepath.Join("testdata", c.file))
			got := CompareBench(cand, base)
			if got.Verdict != c.verdict {
				t.Errorf("verdict = %q, want %q (%s)", got.Verdict, c.verdict, got.Detail)
			}
			if got.CandPass != c.cand || got.BasePass != 3 || got.N != c.n {
				t.Errorf("passes = %d/%d vs %d, want %d/%d vs 3", got.CandPass, got.N, got.BasePass, c.cand, c.n)
			}
			for _, w := range c.detailWant {
				if !strings.Contains(got.Detail, w) {
					t.Errorf("detail %q lacks %q", got.Detail, w)
				}
			}
		})
	}
}

// The result files phase 3's run.sh self-test uses are the ground truth for
// the JSON shape. Parsing them here keeps this package from drifting away from
// what run.sh actually writes, and pins the verdicts to compare.py's exit
// codes on the same inputs (1 → fail, 2 → inconclusive, 0 → pass).
func TestCompareBenchMatchesComparePyFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "evals", "bench", "testdata")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("evals/bench/testdata not present: %v", err)
	}
	cand := loadBench(t, filepath.Join(dir, "cand.json"))
	base := loadBench(t, filepath.Join(dir, "base.json"))
	candErr := loadBench(t, filepath.Join(dir, "cand-error.json"))

	for _, c := range []struct {
		name      string
		cand, bas BenchRun
		want      string
	}{
		{"regression", cand, base, VerdictFail},
		{"run error", candErr, base, VerdictInconclusive},
		{"run error on the baseline side", base, candErr, VerdictInconclusive},
		{"self", base, base, VerdictPass},
	} {
		if got := CompareBench(c.cand, c.bas); got.Verdict != c.want {
			t.Errorf("%s: verdict = %q, want %q (%s)", c.name, got.Verdict, c.want, got.Detail)
		}
	}
}

func benchRun(tasks ...BenchTask) BenchRun {
	return BenchRun{Model: "m", Effort: "e", Tasks: tasks}
}

func TestCompareBenchNotComparable(t *testing.T) {
	a := BenchTask{ID: "a", Pass: true}
	b := BenchTask{ID: "b", Pass: true}
	for _, c := range []struct {
		name       string
		cand, base BenchRun
		want       string
	}{
		{"empty candidate", benchRun(), benchRun(a), "task sets differ"},
		{"empty baseline", benchRun(a), benchRun(), "task sets differ"},
		{"duplicate candidate id", benchRun(a, a), benchRun(a, b), "duplicate task ids — candidate [a]"},
		{"duplicate baseline id", benchRun(a, b), benchRun(a, a), "baseline [a]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := CompareBench(c.cand, c.base)
			if got.Verdict != VerdictInconclusive || !strings.Contains(got.Detail, c.want) {
				t.Errorf("got %q / %q, want inconclusive containing %q", got.Verdict, got.Detail, c.want)
			}
		})
	}
}

// A task marked both pass and error is a broken run, never a pass.
func TestCompareBenchErrorNeverCountsAsPass(t *testing.T) {
	got := CompareBench(
		benchRun(BenchTask{ID: "a", Pass: true, Error: true}),
		benchRun(BenchTask{ID: "a", Pass: true}))
	if got.CandPass != 0 || got.BasePass != 1 {
		t.Errorf("passes = %d vs %d, want 0 vs 1", got.CandPass, got.BasePass)
	}
	if got.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want inconclusive", got.Verdict)
	}
}

func TestLoadBench(t *testing.T) {
	run := loadBench(t, filepath.Join("testdata", "bench-cand-worse.json"))
	if run.Model != "candidate-model" || run.Effort != "medium" || run.ClaudeVersion != "fixture" ||
		run.StartedAt != "2026-09-29T00:00:00Z" || len(run.Tasks) != 5 {
		t.Fatalf("header not parsed: %+v", run)
	}
	b01, b05 := run.Tasks[0], run.Tasks[4]
	if b01.Turns == nil || *b01.Turns != 6 || b01.CostUSD == nil || *b01.CostUSD != 0.21 ||
		b01.DurationMs == nil || *b01.DurationMs != 41000 || b01.ExitCode == nil || *b01.ExitCode != 0 {
		t.Errorf("b01 numbers not parsed: %+v", b01)
	}
	if !b05.TimedOut || b05.Turns != nil || b05.CostUSD != nil || b05.DurationMs != nil || b05.ExitCode != nil {
		t.Errorf("b05 nulls must stay nil: %+v", b05)
	}
	if b05.CheckTail != "timed out after 900s" {
		t.Errorf("checkTail = %q", b05.CheckTail)
	}

	dir := t.TempDir()
	for name, body := range map[string]string{
		"bad-json.json":   `{"tasks": [`,
		"no-tasks.json":   `{"model": "m"}`,
		"blank-id.json":   `{"tasks": [{"id": " ", "pass": true}]}`,
		"wrong-type.json": `{"tasks": [{"id": "a", "turns": "six"}]}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBench(p); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := LoadBench(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: want an error")
	}
}

func TestCombine(t *testing.T) {
	verdicts := []string{VerdictPass, VerdictFail, VerdictInconclusive}
	want := map[[2]string]string{ // {traj, bench} → combined
		{VerdictPass, VerdictPass}:                 VerdictPass,
		{VerdictPass, VerdictFail}:                 VerdictFail,
		{VerdictPass, VerdictInconclusive}:         VerdictPass,
		{VerdictFail, VerdictPass}:                 VerdictFail,
		{VerdictFail, VerdictFail}:                 VerdictFail,
		{VerdictFail, VerdictInconclusive}:         VerdictFail,
		{VerdictInconclusive, VerdictPass}:         VerdictPass,
		{VerdictInconclusive, VerdictFail}:         VerdictFail,
		{VerdictInconclusive, VerdictInconclusive}: VerdictInconclusive,
	}
	for _, tv := range verdicts {
		for _, bv := range verdicts {
			t.Run(tv+"/"+bv, func(t *testing.T) {
				traj := Result{Model: "m", GoldenSetVersion: "v1", Verdict: tv,
					// Evaluate's detail ends with a full stop; Combine drops it at the join.
					Score: 3.1, Trajectories: 7, AgentsCovered: 2, Detail: "traj says " + tv + "."}
				got := Combine(traj, &BenchVerdict{Verdict: bv, Detail: "bench says " + bv})
				if got.Verdict != want[[2]string{tv, bv}] {
					t.Errorf("Combine(%s, %s) = %q, want %q", tv, bv, got.Verdict, want[[2]string{tv, bv}])
				}
				if wantDetail := "traj says " + tv + "; bench says " + bv; got.Detail != wantDetail {
					t.Errorf("detail = %q, want %q", got.Detail, wantDetail)
				}
				if got.Model != "m" || got.GoldenSetVersion != "v1" || got.Score != 3.1 ||
					got.Trajectories != 7 || got.AgentsCovered != 2 {
					t.Errorf("trajectory fields not kept: %+v", got)
				}
			})
		}
	}
	t.Run("nil bench", func(t *testing.T) {
		traj := Result{Model: "m", Verdict: VerdictInconclusive, Detail: "d"}
		if got := Combine(traj, nil); got != traj {
			t.Errorf("Combine(traj, nil) = %+v, want traj unchanged", got)
		}
	})
}

func TestValidateBenchPair(t *testing.T) {
	run := func(model, effort string) BenchRun { return BenchRun{Model: model, Effort: effort} }
	for _, c := range []struct {
		name       string
		model      string
		cand, base BenchRun
		wantErr    string // "" = accepted
	}{
		{"model switch", "claude-opus-5-5", run("claude-opus-5-5", "high"), run("claude-opus-5", "high"), ""},
		{"effort switch", "claude-opus-5-5", run("claude-opus-5-5", "medium"), run("claude-opus-5-5", "high"), ""},
		{"1M-window marker normalised on both sides", "claude-opus-5-5[1m]",
			run("claude-opus-5-5", "high"), run("claude-opus-5", "high"), ""},
		{"candidate marker normalised", "claude-opus-5-5",
			run("claude-opus-5-5[1m]", "high"), run("claude-opus-5", "high"), ""},
		{"candidate is another model", "claude-opus-5-5",
			run("claude-sonnet-5", "high"), run("claude-opus-5", "high"),
			`bench candidate is a run of model "claude-sonnet-5", but --model is "claude-opus-5-5"`},
		{"candidate has no model", "claude-opus-5-5", run("", "high"), run("claude-opus-5", "high"),
			`--model is "claude-opus-5-5"`},
		{"same model and effort", "claude-opus-5-5",
			run("claude-opus-5-5", "high"), run("claude-opus-5-5", "high"), "same configuration (claude-opus-5-5/high)"},
		{"same configuration behind a window marker", "claude-opus-5-5",
			run("claude-opus-5-5", "high"), run("claude-opus-5-5[1m]", "high"), "same configuration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateBenchPair(c.model, c.cand, c.base)
			switch {
			case c.wantErr == "" && err != nil:
				t.Errorf("want accepted, got %v", err)
			case c.wantErr != "" && err == nil:
				t.Errorf("want an error containing %q, got nil", c.wantErr)
			case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
				t.Errorf("error %q lacks %q", err, c.wantErr)
			}
		})
	}
}

func TestFailReason(t *testing.T) {
	trajFail := Result{Verdict: VerdictFail, Detail: "mean 2.1 below baseline"}
	trajPass := Result{Verdict: VerdictPass, Detail: "fine"}
	benchFail := &BenchVerdict{Verdict: VerdictFail, Detail: "bench 2/5 vs 3/5"}
	benchPass := &BenchVerdict{Verdict: VerdictPass, Detail: "bench 3/5 vs 3/5"}
	for _, c := range []struct {
		name  string
		traj  Result
		bench *BenchVerdict
		want  string
	}{
		{"bench only", trajPass, benchFail, "bench regression: bench 2/5 vs 3/5"},
		{"trajectory only", trajFail, benchPass, "trajectory regression: mean 2.1 below baseline"},
		{"trajectory, no bench", trajFail, nil, "trajectory regression: mean 2.1 below baseline"},
		{"both", trajFail, benchFail,
			"bench regression: bench 2/5 vs 3/5; trajectory regression: mean 2.1 below baseline"},
		{"neither", trajPass, benchPass, ""},
	} {
		if got := FailReason(c.traj, c.bench); got != c.want {
			t.Errorf("%s: FailReason = %q, want %q", c.name, got, c.want)
		}
	}
}
