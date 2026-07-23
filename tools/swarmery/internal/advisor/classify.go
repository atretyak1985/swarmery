package advisor

import "strings"

// ErrClass buckets a normalized error-group key (normalizeErrKey output) by
// what it says about agent quality. Unknown keys default to BehaviorFixable —
// conservative: visible in metrics, never silently dropped as noise.
type ErrClass string

const (
	InfraNoise         ErrClass = "infra_noise"         // network/API — not the agent's fault
	HarnessRecoverable ErrClass = "harness_recoverable" // harness rule hit, agent self-recovers
	BehaviorFixable    ErrClass = "behavior_fixable"    // prompt-fixable agent behavior
	OutcomeFailure     ErrClass = "outcome_failure"     // reserved: verdict/re-dispatch signals (R4), not error events
)

// classPrefixes is prefix-matched against normalizeErrKey output (lowercased,
// digit runs folded to "#"). The table is normative — see the phase-1 plan doc.
var classPrefixes = []struct {
	prefix string
	class  ErrClass
}{
	{"connection error", InfraNoise},
	{`# {"type":"error","error":{"type":"overloaded_error"`, InfraNoise},
	{"request timed out", InfraNoise},
	{"error: file has not been read yet", HarnessRecoverable},
	{"error: file has been modified since read", HarnessRecoverable},
	{"error: file does not exist", HarnessRecoverable},
	{"error: permission for this action was denied by the claude code auto mode", HarnessRecoverable},
	{"error: this agent is isolated in the worktree", HarnessRecoverable},
	{"error: exit code #", BehaviorFixable}, // includes normal failing tests; stays visible but see the R2 note
	{"error: found # matches of the string to replace", BehaviorFixable},
	{"inputvalidationerror", BehaviorFixable},
	{"error: subagents should return findings as text", BehaviorFixable},
	{"bash error", BehaviorFixable},
}

// Classify maps one error-group key to its ErrClass. Keys are lowercased
// before matching (normalizeErrKey output already is; raw callers may not be).
func Classify(groupKey string) ErrClass {
	k := strings.ToLower(groupKey)
	for _, p := range classPrefixes {
		if strings.HasPrefix(k, p.prefix) {
			return p.class
		}
	}
	return BehaviorFixable
}
