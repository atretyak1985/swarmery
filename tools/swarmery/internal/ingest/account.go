package ingest

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultAccount is the display/filter key of the stock subscription — the
// one whose config dir is plain ~/.claude. It is also what a stored ” means
// to a reader: rows ingested before the account column existed, and every
// session minted through the hooks channel (which has no config dir to derive
// from), carry ” and are shown as the default account.
const DefaultAccount = "default"

// AccountFor derives the subscription ("account") key of a transcript from the
// projects root it was discovered under.
//
// originRoot is "<configDir>/projects". The key is the basename of <configDir>
// with the ".claude" prefix stripped and any leading '-' or '.' trimmed off
// what remains; an empty remainder is the stock config dir and yields
// DefaultAccount:
//
//	~/.claude/projects          → "default"
//	~/.claude-nabu-org/projects   → "nabu-org"
//	~/.claude-science/projects  → "science"
//
// An UNKNOWN root — "" — returns "", not DefaultAccount. Callers with no root
// context (the single-file `swarmery ingest`, the hooks path, tests) must
// leave the column at its ” default so a later tail that DOES know the root
// can still stamp the row; writing "default" there would freeze a guess into
// the data. Readers fold the two together, so the distinction is invisible in
// the UI and load-bearing only at write time.
func AccountFor(originRoot string) string {
	root := strings.TrimSpace(originRoot)
	if root == "" {
		return ""
	}
	// Clean first: it drops trailing separators and "." segments, so
	// ".../projects/", ".../projects" and ".../.claude-x/./projects" all name
	// the same config dir.
	name := filepath.Base(filepath.Dir(filepath.Clean(root)))
	if name == "." || name == string(filepath.Separator) {
		return DefaultAccount // a rootless/relative root names no config dir
	}
	key := strings.TrimLeft(strings.TrimPrefix(name, ".claude"), "-.")
	if key == "" {
		return DefaultAccount
	}
	return key
}

// UnboundAccount is the AccountFor key of the config dir an UNBOUND project's
// runs execute under: the CLAUDE_CONFIG_DIR this process inherited (an unbound
// run keeps it), or DefaultAccount when it inherited none. Those runs are
// admitted and paused under DefaultAccount whatever this returns, so a failure
// seen in the transcripts of THIS dir must pause DefaultAccount too.
func UnboundAccount() string {
	dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if dir == "" {
		return DefaultAccount
	}
	return AccountFor(filepath.Join(dir, "projects"))
}
