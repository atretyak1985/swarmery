package claudeacct

// Lock 2 — the STORE ANCHOR: a credential store names the directory trees it
// may be released to, and nothing else can widen that.
//
// # The residual Lock 1 cannot close
//
// Lock 1 (gittracked.go) ignores a binding that git tracks, so a clone cannot
// choose whose credentials it gets. A tree with no .git — an extracted tarball,
// a copied directory — carries its binding as an ordinary untracked file, and
// Lock 1 has no way to tell it from the operator's own. The store anchor closes
// that for CREDENTIALS: a store that carries
//
//	# swarmery-root: /abs/path/of/the/tree
//
// is released only to a rung inside one of the trees it names. The operator
// writes those lines — swarmery never writes a store — so the set of trees a
// store reaches is decided by the one file that holds the secrets.
//
// # The rules, stated once
//
//   - A root line is a comment to every older loader (parseSecretEnv skips '#'
//     lines), so anchoring a store never breaks a binary that predates this.
//   - Any root line makes the store ROOTED, valid or not. A relative,
//     unresolvable or non-directory root admits nothing — fail closed — and is
//     reported by line number and reason, never silently dropped.
//   - Root lines are read through openStore, the SAME checks the secrets pass:
//     a store the loader refuses is read as rootless (it releases no names
//     anyway), never as "rooted somewhere".
//   - Admission compares FILES, not strings: the rung is resolved with
//     EvalSymlinks, and it and each of its ancestors is compared to each
//     resolved root with os.SameFile. A string prefix would admit the sibling
//     /x/ae-evil for the root /x/ae and miss an APFS case variant; walking the
//     SYMBOLIC rung's ancestors would admit a link planted inside the root that
//     points outside it.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// storeRootMarker is the name inside a root line: `# swarmery-root: <abs>`.
// Whitespace after the '#' is optional, so a hand-written `#swarmery-root:`
// still counts — recognising more spellings can only make a store MORE rooted,
// which is the fail-closed direction.
const storeRootMarker = "swarmery-root:"

// storeRoots is one store's root lines as the loader sees the file.
type storeRoots struct {
	state  StoreState // the loader's verdict on the file (absent, present, refused)
	rooted bool       // at least one root line, valid or not
	roots  []string   // the usable roots: absolute, resolved directories
	bad    []string   // "line N: <reason>" for every root line that admits nothing
}

// readStoreRoots reads the root lines of the store at path. It logs nothing:
// Resolve calls it, and Resolve never logs. The file's VARIABLES are neither
// parsed nor kept here.
func readStoreRoots(path string) storeRoots {
	if path == "" {
		return storeRoots{state: StoreAbsent}
	}
	f, c := openStore(path)
	if f == nil {
		return storeRoots{state: c.State}
	}
	defer f.Close()
	out := storeRoots{state: StorePresent}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		raw, ok := rootLineValue(sc.Text())
		if !ok {
			continue
		}
		out.rooted = true
		if root, why := usableRoot(raw); why != "" {
			out.bad = append(out.bad, fmt.Sprintf("line %d: %s", n, why))
		} else {
			out.roots = append(out.roots, root)
		}
	}
	if err := sc.Err(); err != nil {
		// A store that cannot be read to the end is rooted by whatever it had
		// declared so far, plus a line that admits nothing: never less rooted.
		out.rooted = true
		out.bad = append(out.bad, fmt.Sprintf("read error: %v", unwrapPathErr(err)))
	}
	return out
}

// rootLineValue returns the path a root line carries, and whether the line is
// a root line at all. The marker is matched loosely — any case, `-`, `_` or a
// space between the words, a leading byte-order mark — because a misspelled
// marker must make a store MORE rooted (it then admits nothing), never leave
// it rootless and released everywhere.
func rootLineValue(line string) (string, bool) {
	line = strings.TrimPrefix(strings.TrimSpace(line), "\ufeff")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	const words = len("swarmery-root:")
	if len(rest) < words {
		return "", false
	}
	switch strings.ToLower(rest[:words]) {
	case "swarmery-root:", "swarmery_root:", "swarmery root:":
		return strings.TrimSpace(rest[words:]), true
	}
	return "", false
}

// usableRoot is a root line's value, cleaned — or why it admits nothing.
func usableRoot(raw string) (string, string) {
	switch {
	case raw == "":
		return "", "the root line names no path"
	case !filepath.IsAbs(raw):
		return "", fmt.Sprintf("%q is not an absolute path", raw)
	}
	root := filepath.Clean(raw)
	res, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Sprintf("%s cannot be resolved (%v)", root, unwrapPathErr(err))
	}
	fi, err := os.Stat(res)
	if err != nil || !fi.IsDir() {
		return "", fmt.Sprintf("%s is not a directory", root)
	}
	if tooBroad(fi, res) {
		return "", fmt.Sprintf("%s contains the home directory, so it would admit every clone and download under it", root)
	}
	return root, ""
}

// tooBroad reports whether a root would admit the home directory — "/" or
// $HOME itself, or any ancestor of it. Such a root admits every archive and
// clone that lands under home, which is exactly what the anchor exists to stop.
func tooBroad(rootInfo os.FileInfo, res string) bool {
	if filepath.Dir(res) == res {
		return true
	}
	h, err := userHomeDir()
	if err != nil || strings.TrimSpace(h) == "" {
		return false
	}
	hr, err := filepath.EvalSymlinks(h)
	if err != nil {
		return false
	}
	for dir := hr; ; {
		if fi, err := os.Stat(dir); err == nil && os.SameFile(fi, rootInfo) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// admits returns the root that admits rung, or "" when none does. See the file
// header for why this compares resolved files and never strings.
func (s storeRoots) admits(rung string) string {
	return admits(rung, s.roots)
}

// admitsPhysical is admits for the path a walk ran from, which may not exist
// yet (the ladder lets a not-yet-created project inherit its ancestors): such a
// path is physically wherever its nearest EXISTING ancestor resolves, so that
// ancestor is what is tested.
//
// p is taken as given, never lexically cleaned: `root/link/../x` is physically
// wherever link's target's parent holds x, and only the kernel's walk (Lstat,
// EvalSymlinks) knows that; filepath.Clean would put it at root/x.
func (s storeRoots) admitsPhysical(p string) string {
	for {
		if _, err := os.Lstat(p); err == nil {
			return admits(p, s.roots)
		}
		i := strings.LastIndexByte(strings.TrimRight(p, string(filepath.Separator)), filepath.Separator)
		if i <= 0 {
			return ""
		}
		p = p[:i]
	}
}

// WithinRoot reports whether path lies inside root (root itself included), by
// the same resolved-file comparison Lock 2's admission uses: EvalSymlinks(path),
// then it and each of its ancestors against the resolved root with os.SameFile —
// never a string prefix, which would admit the sibling /x/ae-evil for /x/ae and
// miss an APFS case variant. False for an empty or unresolvable path or root.
// internal/runsettings uses it to refuse an estate settings file that a symlink
// carries out of its estate.
func WithinRoot(path, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	return admits(path, []string{root}) != ""
}

// admits is the admission test: rr = EvalSymlinks(rung); rr and each of its
// ancestors, nearest first, against each resolved root, by os.SameFile. An
// empty or unresolvable rung is never admitted.
func admits(rung string, roots []string) string {
	if strings.TrimSpace(rung) == "" || len(roots) == 0 {
		return ""
	}
	// Absolute paths go to EvalSymlinks as given: it resolves each link before
	// applying a following "..", which a lexical Clean would not.
	p := rung
	if !filepath.IsAbs(p) {
		p = cleanAbs(p)
	}
	rr, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	type resolvedRoot struct {
		root string
		info os.FileInfo
	}
	var rs []resolvedRoot
	for _, root := range roots {
		res, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		fi, err := os.Stat(res)
		if err != nil || !fi.IsDir() {
			continue
		}
		rs = append(rs, resolvedRoot{root, fi})
	}
	for dir := rr; ; {
		if fi, err := os.Stat(dir); err == nil {
			for _, r := range rs {
				if os.SameFile(fi, r.info) {
					return r.root
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ── the release table (D5) ───────────────────────────────────────────────────

// The admission lines `swarmery account which` prints. The wording is a
// contract: operators grep for it and the phase criteria search for it.
func admittedLine(key, root string) string {
	return fmt.Sprintf("%s.env admitted by root %s", key, root)
}
func notAdmittedLine(key string) string { return fmt.Sprintf("not admitted by %s.env roots", key) }
func rootlessLine(key string) string    { return fmt.Sprintf("%s.env rootless", key) }
func unanchoredLine(key string) string  { return fmt.Sprintf("estate %s unanchored", key) }

// admission is the release table applied to one Resolution: which stores a
// spawn under it receives, and the lines that say why.
type admission struct {
	accountReleased bool
	// accountRootlessWarn is the store path to WARN about (store-rootless) when
	// the account store was released only because it carries no root line and
	// a RUNG decided the account. "" otherwise.
	accountRootlessWarn string
	accountNote         string

	estateReleased bool
	estateNote     string
}

// admit applies D5's release table to r. It reads the two stores' root lines
// and logs nothing; the WARN it asks for is logged by the composer.
//
//   - Account store <Account>.env — `default` and "" have none.
//     From a rung (Resolve): rooted → released iff its roots admit AccountRoot;
//     rootless → released, and a store-rootless WARN.
//     Forced (WithAccount): released only if the cwd independently resolved the
//     SAME key, and then by the rule above against the cwd's rung.
//     Key-only (SpawnEnv(key), no rung): rootless → released; rooted → not.
//   - Estate store <Estate>.env: released iff ROOTED and its roots admit
//     EstateRoot. Absent, rootless or refused: nothing — the unanchored state,
//     zero credentials and no estate settings, never an error.
//   - The payer (CLAUDE_CONFIG_DIR) is not decided here: it follows the
//     Lock-1-clean binding and is never gated by roots (residual R13).
//
// Two refinements close what the table alone leaves open:
//
//   - A rooted store must admit the rung AND the resolved path the walk ran
//     from: the ladder climbs logical ancestors, so `<root>/link -> ~/outside`
//     would otherwise put a physically foreign directory under the root.
//   - A rootless store is released through the ACCOUNT route from a RUNG only
//     for a key whose account has completed a login on this machine
//     (accountLoggedIn): the store namespace is shared, and a binding naming an
//     estate key as its "account" must not pull an unanchored estate store out
//     that way. A config dir merely existing is not enough — R13 lets such a
//     binding pick the payer, and one unauthenticated `claude` run under it
//     creates <dir>/projects. The key-only route (the dashboard's account
//     terminal, where the operator picked a discovered account) is not a rung
//     and keeps today's behaviour.
func admit(r Resolution) admission {
	var a admission
	if key := strings.TrimSpace(r.Account); key != "" && key != ingest.DefaultAccount {
		rung, forcedMismatch := r.AccountRoot, false
		if r.Source == SourceForced {
			rung = r.cwdAccountRoot
			forcedMismatch = strings.TrimSpace(r.cwdAccount) != key
		}
		phys := r.physical
		if phys == "" {
			phys = rung
		}
		path := SecretsPath(key)
		sr := readStoreRoots(path)
		switch {
		case sr.state == StoreAbsent:
			// no store: nothing to release and nothing to say
		case sr.state == StoreRefused:
			// Read as rootless, and the loader releases no names from it anyway;
			// the loader's own log line says what to fix.
			a.accountNote = fmt.Sprintf("%s.env refused by the loader", key)
		case forcedMismatch:
			a.accountNote = fmt.Sprintf("%s not released: the account is forced, and this directory resolves %s",
				key+".env", orDefault(r.cwdAccount))
		case !sr.rooted && rung != "" && !accountLoggedIn(key):
			a.accountNote = fmt.Sprintf("%s.env rootless, and %s is no logged-in account on this machine — not released", key, key)
		case !sr.rooted:
			a.accountReleased = true
			a.accountNote = rootlessLine(key)
			if rung != "" {
				a.accountRootlessWarn = path
			}
		default:
			if root := sr.admits(rung); root != "" && sr.admitsPhysical(phys) != "" {
				a.accountReleased = true
				a.accountNote = admittedLine(key, root)
			} else {
				a.accountNote = notAdmittedLine(key)
			}
		}
	}
	if key := strings.TrimSpace(r.Estate); key != "" {
		sr := readStoreRoots(SecretsPath(key))
		switch {
		case sr.state == StorePresent && sr.rooted:
			phys := r.physical
			if phys == "" {
				phys = r.EstateRoot
			}
			if root := sr.admits(r.EstateRoot); root != "" && sr.admitsPhysical(phys) != "" {
				a.estateReleased = true
				a.estateNote = admittedLine(key, root)
			} else {
				a.estateNote = notAdmittedLine(key)
			}
		default:
			a.estateNote = unanchoredLine(key)
		}
	}
	return a
}

// accountLoggedIn reports whether key is an account on this machine that has
// completed a login: Discover finds its config dir, and that dir's
// .claude.json — a regular file owned by the current user — carries a
// non-empty top-level `oauthAccount` object, which the CLI writes at login and
// an unauthenticated run does not. The file can be large, so it is scanned
// token by token, never read whole; any doubt answers false.
func accountLoggedIn(key string) bool {
	for _, a := range Discover() {
		if a.Key == key && !a.IsDefault {
			return hasOAuthAccount(filepath.Join(a.ConfigDir, ".claude.json"))
		}
	}
	return false
}

// maxProfileBytes bounds how much of a .claude.json the login check reads.
const maxProfileBytes = 64 << 20

func hasOAuthAccount(path string) bool {
	f, err := openNoFollow(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if uid, ok := fileOwner(fi); !ok || uid != currentUID() {
		return false
	}
	dec := json.NewDecoder(io.LimitReader(f, maxProfileBytes))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		name, _ := t.(string)
		if name != "oauthAccount" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return false
			}
			continue
		}
		var v map[string]any
		return dec.Decode(&v) == nil && len(v) > 0
	}
	return false
}

// orDefault names an unpinned resolution the way every surface does.
func orDefault(key string) string {
	if strings.TrimSpace(key) == "" {
		return ingest.DefaultAccount
	}
	return key
}

// RootLineFor is the exact line an operator adds to a store so it admits dir:
// the directory's RESOLVED path, because admission compares resolved paths.
func RootLineFor(dir string) string {
	p := cleanAbs(dir)
	if res, err := filepath.EvalSymlinks(p); err == nil {
		p = res
	}
	return "# " + storeRootMarker + " " + p
}

// StoreAdmits reports whether store <key>.env is released to a binding at dir,
// and the line to add when it is rooted and is not. Rootless or absent stores
// are "released" here in the account-layer sense (rootless keeps today's
// behaviour); the caller decides what to say. It never reads a variable.
func StoreAdmits(key, dir string) (rooted, admitted bool, addLine string) {
	sr := readStoreRoots(SecretsPath(key))
	if sr.state != StorePresent || !sr.rooted {
		return false, sr.state == StorePresent, ""
	}
	if sr.admits(dir) != "" {
		return true, true, ""
	}
	return true, false, RootLineFor(dir)
}

// StoreRootProblems lists the root lines of store <key>.env that admit nothing,
// by line number and reason — for a surface that must say why a store it
// expected to be anchored is not. Never a variable.
func StoreRootProblems(key string) []string {
	return readStoreRoots(SecretsPath(key)).bad
}
