package accountprune

// The prune's three git states — a thin mapping over the ONE hardened probe,
// claudeacct.FileProvenance (internal/claudeacct/gittracked.go: scrubbed GIT_*
// env, core.fsmonitor= and core.hooksPath=/dev/null, 2 s timeout, literal
// pathspec, every symlink hop probed). This file runs no git of its own: a
// naive `git ls-files` would execute the target repository's fsmonitor.

import "github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"

// GitStatus is how git sees one settings file. The spellings are the tokens
// every dry-run line ends in.
type GitStatus string

const (
	// StatusTracked is a file git tracks — or one whose status git cannot
	// tell (fail closed): writing it would push this machine's config at
	// everyone who pulls, so the prune refuses unless told otherwise.
	StatusTracked GitStatus = "TRACKED"
	// StatusUntracked is a file inside a repository that git does not track
	// (gitignored counts as untracked).
	StatusUntracked GitStatus = "untracked"
	// StatusNoRepo is a file outside every repository.
	StatusNoRepo GitStatus = "NO-REPO"
)

// probe is the provenance probe. A var so a test can stand in an
// indeterminate verdict; production never replaces it.
var probe = claudeacct.FileProvenance

// gitStatus classifies path.
func gitStatus(path string) GitStatus {
	v, _ := probe(path)
	return statusFor(v)
}

// statusFor maps a probe verdict onto the three states. Anything that is not
// positively untracked or outside a repository — tracked, unknown, or a verdict
// added later — is TRACKED: the refusal is the safe side.
func statusFor(v claudeacct.Provenance) GitStatus {
	switch v {
	case claudeacct.ProvenanceUntracked:
		return StatusUntracked
	case claudeacct.ProvenanceNotRepo:
		return StatusNoRepo
	}
	return StatusTracked
}
