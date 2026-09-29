package modeleval

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
)

// The three verdicts every result in this package carries. Evaluate predates
// these names and still spells them as literals; the values are identical, so
// a Result from Evaluate and one from Combine compare equal on Verdict.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictInconclusive = "inconclusive"
)

// BenchTask is one task of a frozen-bench result file, exactly as
// evals/bench/run.sh writes it. turns, costUsd, durationMs and exitCode are
// null when the session produced no result (a timeout or a broken run), hence
// the pointers: a missing number is not a zero.
type BenchTask struct {
	ID         string   `json:"id"`
	Pass       bool     `json:"pass"`
	Error      bool     `json:"error"`
	TimedOut   bool     `json:"timedOut"`
	ExitCode   *int     `json:"exitCode"`
	Turns      *int     `json:"turns"`
	CostUSD    *float64 `json:"costUsd"`
	DurationMs *int64   `json:"durationMs"`
	CheckTail  string   `json:"checkTail"`
}

// BenchRun is one whole bench result file: one configuration (model × effort)
// run over the task suite.
type BenchRun struct {
	Model         string      `json:"model"`
	Effort        string      `json:"effort"`
	StartedAt     string      `json:"startedAt"`
	ClaudeVersion string      `json:"claudeVersion"`
	Tasks         []BenchTask `json:"tasks"`
}

// LoadBench reads and shape-checks a bench result file. It rejects what
// compare.py calls unreadable input (bad JSON, no tasks array, a task without
// an id) and leaves comparability — duplicates, differing sets, run errors —
// to CompareBench, which reports it as a verdict rather than an error.
func LoadBench(path string) (BenchRun, error) {
	var run BenchRun
	raw, err := os.ReadFile(path)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return run, fmt.Errorf("bench %s: %w", path, err)
	}
	if run.Tasks == nil {
		return run, fmt.Errorf("bench %s: no \"tasks\" array", path)
	}
	for i, t := range run.Tasks {
		if strings.TrimSpace(t.ID) == "" {
			return run, fmt.Errorf("bench %s: task %d has no id", path, i)
		}
	}
	return run, nil
}

// ValidateBenchPair refuses a bench pair that cannot speak for model:
//
//   - the candidate run must be OF the model being evaluated, compared after
//     the same modelid.Base normalisation Evaluate applies, or a bench on one
//     model would decide the verdict persisted for another;
//   - candidate and baseline must differ in model or effort, because a run
//     compared with the same configuration proves nothing about a switch.
func ValidateBenchPair(model string, cand, base BenchRun) error {
	want, got := modelid.Base(model), modelid.Base(cand.Model)
	if got != want {
		return fmt.Errorf("bench candidate is a run of model %q, but --model is %q", got, want)
	}
	if modelid.Base(base.Model) == got && strings.TrimSpace(base.Effort) == strings.TrimSpace(cand.Effort) {
		return fmt.Errorf("bench candidate and baseline are the same configuration (%s/%s): "+
			"comparing a run with itself proves nothing", got, strings.TrimSpace(cand.Effort))
	}
	return nil
}

// BenchVerdict is the outcome of comparing a candidate bench run against a
// baseline run of the same suite.
type BenchVerdict struct {
	Verdict   string  `json:"verdict"`
	CandPass  int     `json:"candPass"`
	BasePass  int     `json:"basePass"`
	N         int     `json:"n"`
	CandCost  float64 `json:"candCost"`
	BaseCost  float64 `json:"baseCost"`
	CandTurns int     `json:"candTurns"`
	BaseTurns int     `json:"baseTurns"`
	Detail    string  `json:"detail"`
}

// benchTotals is what one side of a comparison adds up to.
type benchTotals struct {
	pass, turns int
	cost        float64
	nulls       bool // some task had no turns or cost — counted as 0
}

func totals(run BenchRun) benchTotals {
	var t benchTotals
	for _, task := range run.Tasks {
		// A run error is infrastructure, not the model, so it never counts as a
		// pass even if the file somehow says pass:true.
		if task.Pass && !task.Error {
			t.pass++
		}
		if task.Turns != nil {
			t.turns += *task.Turns
		} else {
			t.nulls = true
		}
		if task.CostUSD != nil {
			t.cost += *task.CostUSD
		} else {
			t.nulls = true
		}
	}
	return t
}

// duplicateIDs returns the ids that occur more than once, sorted.
func duplicateIDs(run BenchRun) []string {
	seen := map[string]int{}
	for _, t := range run.Tasks {
		seen[t.ID]++
	}
	var dups []string
	for id, n := range seen {
		if n > 1 {
			dups = append(dups, id)
		}
	}
	sort.Strings(dups)
	return dups
}

// idsOnlyIn returns the task ids of a that b lacks, sorted.
func idsOnlyIn(a, b BenchRun) []string {
	inB := map[string]bool{}
	for _, t := range b.Tasks {
		inB[t.ID] = true
	}
	var only []string
	for _, t := range a.Tasks {
		if !inB[t.ID] {
			only = append(only, t.ID)
		}
	}
	sort.Strings(only)
	return only
}

// erroredIDs returns the ids whose run broke on either side, sorted.
func erroredIDs(cand, base BenchRun) []string {
	set := map[string]bool{}
	for _, run := range []BenchRun{cand, base} {
		for _, t := range run.Tasks {
			if t.Error {
				set[t.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// notComparable reports why two runs cannot be compared, or "" when they can.
// The order mirrors evals/bench/compare.py: duplicates, then differing sets,
// then run errors.
func notComparable(cand, base BenchRun) string {
	if len(cand.Tasks) == 0 || len(base.Tasks) == 0 {
		return "not comparable: task sets differ (a side has no tasks)"
	}
	candDups, baseDups := duplicateIDs(cand), duplicateIDs(base)
	if len(candDups) > 0 || len(baseDups) > 0 {
		return fmt.Sprintf("not comparable: duplicate task ids — candidate %v, baseline %v",
			candDups, baseDups)
	}
	onlyC, onlyB := idsOnlyIn(cand, base), idsOnlyIn(base, cand)
	if len(onlyC) > 0 || len(onlyB) > 0 {
		return fmt.Sprintf("not comparable: task sets differ — candidate-only %v, baseline-only %v",
			onlyC, onlyB)
	}
	if errored := erroredIDs(cand, base); len(errored) > 0 {
		return fmt.Sprintf("not comparable: run error in %s (infrastructure, not the model); re-run those tasks",
			strings.Join(errored, ", "))
	}
	return ""
}

// CompareBench decides whether the candidate regressed against the baseline on
// identical work, with the same semantics as evals/bench/compare.py:
//
//   - duplicate ids, differing id sets, an empty side, or any error:true task
//     on either side → inconclusive (the runs are not comparable; re-run);
//   - candidate passes fewer tasks → fail;
//   - otherwise → pass.
//
// Turns and cost are reported, never gated: spend is the operator's call.
func CompareBench(cand, base BenchRun) BenchVerdict {
	ct, bt := totals(cand), totals(base)
	v := BenchVerdict{
		CandPass: ct.pass, BasePass: bt.pass, N: len(cand.Tasks),
		CandCost: ct.cost, BaseCost: bt.cost,
		CandTurns: ct.turns, BaseTurns: bt.turns,
	}
	numbers := fmt.Sprintf("bench %d/%d vs %d/%d, turns %d vs %d, cost $%.2f vs $%.2f (%s/%s vs %s/%s)",
		ct.pass, len(cand.Tasks), bt.pass, len(base.Tasks), ct.turns, bt.turns, ct.cost, bt.cost,
		cand.Model, cand.Effort, base.Model, base.Effort)
	if ct.nulls || bt.nulls {
		numbers += ", tasks without a result counted as 0 turns and $0.00"
	}

	if reason := notComparable(cand, base); reason != "" {
		v.Verdict = VerdictInconclusive
		v.Detail = reason + "; " + numbers
		return v
	}
	if ct.pass < bt.pass {
		v.Verdict = VerdictFail
	} else {
		v.Verdict = VerdictPass
	}
	v.Detail = numbers
	return v
}
