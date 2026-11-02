package phaserun

// The fleet roles a daemon phase run spawns, by their agent registry key (the
// file name under plugins/<pack>/agents/, which is also what
// agent_change_proposals.agent stores). A phase run is an executor turn
// (implementation-agent's leaf-executor contract, restated by BuildPrompt), the
// independent review (reviewerRole: code-reviewer.md), and the deterministic
// verifier (internal/verify: verification-agent's contract).
//
// Consumers that need "would an edit to this agent change how phase runs
// behave" read this list rather than keeping a copy — internal/lessons uses it
// to flag an applied agent rewrite as a confounder of a lesson's measured effect.
const (
	RoleExecutor = "implementation-agent"
	RoleReviewer = "code-reviewer"
	RoleVerifier = "verification-agent"
)

// SpawnedRoles returns the roles above, in spawn order. A fresh slice each call.
func SpawnedRoles() []string {
	return []string{RoleExecutor, RoleReviewer, RoleVerifier}
}
