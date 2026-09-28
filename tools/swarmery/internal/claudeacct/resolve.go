package claudeacct

// Resolve — the ONE answer to "what does a process started in this directory
// run under", on TWO independent axes:
//
//   - the ACCOUNT (the payer): which Claude Code config dir, i.e. which
//     subscription, a spawn uses. Declared by `swarmery.claudeAccount`.
//   - the ESTATE (the project tree): which credential store and which estate
//     settings file travel with the work. Declared by `swarmery.estate`.
//
// Both live in the same binding file (<dir>/.claude/settings.local.json, the
// `swarmery` namespace binding.go owns), and both are found by the same bounded
// upward walk — but each takes the FIRST candidate that declares IT, so the rung
// that decided the account says nothing about the estate. That independence is
// the whole point: a sub-repo pinned to one account on its own rung still
// inherits the estate its ancestor declares, and switching the payer never moves
// a project's credentials.
//
// # What `estate: "<key>"` does
//
// Three independent things:
//
//  1. it makes the declaring directory the ESTATE ROOT, so every descendant
//     resolves to it;
//  2. it selects the credential store <SecretsDir>/<key>.env WHEN THAT FILE
//     EXISTS;
//  3. it selects <estate-root>/.claude/settings.json as SettingsFile when that
//     file exists (a later consumer delivers it; this package only finds it).
//
// An estate with no credential store is a first-class, healthy state: absence
// means zero credentials, never an error, a warning or a log line. A typo in
// the key is caught by coverage elsewhere, not by the store's presence.
//
// # Nested estates subtract, they do not accumulate
//
// Resolve is first-hit. An `estate` declared at a sub-root REPLACES the
// ancestor's key, root AND settings file wholesale; the ancestor's store is NOT
// merged in, and the sub-estate composes its own store and nothing else. This
// is a deliberate, documented limit, not an oversight: a sub-tree that needs one
// more credential puts that name in its own store, which is what an operator
// does anyway. Revisit only if a third estate or a shared-credential rotation
// makes it hurt. Do not "fix" it into a merge.
//
// # The walk, and why it cannot capture what the operator did not write
//
// One ordered candidate list: the project path itself, then each ancestor via
// filepath.Dir. The list stops BEFORE $HOME (the home directory is never a
// candidate, so ~/.claude/settings.local.json can never be read as a
// declaration), never includes the filesystem root, stops at a filepath.Dir
// fixed point, is capped at maxLadderRungs, and STOPS at the first existing
// directory not owned by the current user — which is what bounds a path outside
// $HOME (/tmp, /Users/Shared) to the part of it this user owns. A candidate only
// ever contributes when its binding file is a regular file owned by the current
// user, not writable by group or other, no larger than maxSettingsBytes, and
// carries a `swarmery` object with a field that passes ValidKey; any other file
// means "nothing declared here" and the walk continues past it. Resolve returns
// no error, ever: a spawn must never fail because an optional file is bad.
//
// A path under the daemon's worktree root (<home>/.swarmery/worktrees/…) sits
// outside every project tree, so Resolve first maps it back to its SOURCE
// checkout (worktreesrc.go) and walks from there.
//
// # The two locks (D5)
//
// Lock 1 — provenance — runs at EVERY rung, inside namespaceAt: a binding file
// that git tracks (or whose status git cannot tell) declares nothing, for every
// field of the `swarmery` object and every key, `default` included. The rung
// reads as "nothing declared here", the walk goes on, and the reason is kept in
// IgnoredNote. Lock 2 — the store anchor (storeroot.go) — decides which stores
// a resolution receives: AccountStoreAdmitted, EstateAdmitted, AdmissionNote.
// SettingsFile is set only for an admitted estate, so every consumer of it (a
// settings composer, the doctor, an overlay) inherits the gate.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// The Source values Resolve reports for the ACCOUNT axis.
const (
	SourcePin       = "pin"         // the project path's own binding file pins the account
	SourcePinParent = "pin(parent)" // an ancestor's binding file pins it
	SourceDefault   = "default"     // no candidate pins one: the default account
	SourceNone      = "none"        // no project path at all: nothing was resolved
	// SourceForced is set only by WithAccount: the payer was decided by the
	// caller (a sessions row), not by any binding file.
	SourceForced = "forced"
)

// estateField is the declaration field, a sibling of bindingField inside the
// `swarmery` namespace:
//
//	{ "swarmery": { "claudeAccount": "work", "estate": "acme" } }
const estateField = "estate"

// ProjectSettingsFile is the settings file an estate root contributes, relative
// to the root.
const ProjectSettingsFile = ".claude/settings.json"

// maxLadderRungs caps the upward walk. A real path is far shallower; the cap
// exists so no input — a pathological symlink farm, a very deep checkout — can
// make resolution unbounded.
const maxLadderRungs = 64

// Resolution is the resolved answer for one directory. Every field is derived
// from files on disk; nothing here opens a socket or a database.
type Resolution struct {
	Account   string // "" (nothing pinned) | "default" | key
	Source    string // SourcePin | SourcePinParent | SourceDefault | SourceNone (| SourceForced)
	ConfigDir string // "" for the default account — the CLI selects ~/.claude by ABSENCE
	// DefaultProfile is EXPLICIT, true when the effective account is "" or
	// ingest.DefaultAccount. It is never to be inferred from an empty ConfigDir:
	// the default account is a real profile with its own credential and its own
	// .claude.json, and a caller must be able to tell "default" from "unresolved".
	DefaultProfile bool

	// AccountRoot is the directory whose binding file pinned Account ("" when
	// nothing did). Reported so a surface can say WHICH rung won.
	AccountRoot string

	EstateRoot   string // the directory whose binding file declared `estate`, or ""
	Estate       string // the ESTATE key — independent of which rung set Account
	SettingsFile string // <EstateRoot>/.claude/settings.json when it exists, or ""
	// EstateSettingsIsProjects is true when SettingsFile and the project path's
	// own .claude/settings.json are the SAME FILE, decided by resolved absolute
	// path (SameFile), never by string compare or by which rung matched. A
	// settings composer must then count that file ONCE, not twice at two
	// precedences. False whenever SettingsFile is "".
	EstateSettingsIsProjects bool

	// envPath is the resolved credential-store file, set only when something
	// exists at it (whether or not the loader accepts it — CredentialStore). It
	// is unexported on purpose and MUST NEVER reach a printer: surfaces report a
	// COUNT of names (CredentialCount), never a store path.
	envPath string

	// AccountStoreAdmitted is true when a spawn under this resolution receives
	// the account's store <Account>.env (storeroot.go's release table).
	AccountStoreAdmitted bool
	// EstateAdmitted is true when the estate's store is ROOTED and admits
	// EstateRoot: only then do its credentials and SettingsFile flow.
	EstateAdmitted bool
	// AdmissionNote is one line per store that has something to say, in the
	// wording `swarmery account which` prints ("<key>.env admitted by root <r>",
	// "not admitted by <key>.env roots", "<key>.env rootless", "estate <key>
	// unanchored"). Paths and reasons only — never a store's contents.
	AdmissionNote string
	// IgnoredNote is one "<binding path>\t<reason>" line per rung whose binding
	// Lock 1 ignored; IgnoredRungs parses it. Never the file's contents.
	IgnoredNote string

	// cwdAccount and cwdAccountRoot are what the directory itself resolved the
	// account to, kept by WithAccount so the forced rule can compare keys. Both
	// strings: Resolution must stay comparable with ==.
	cwdAccount, cwdAccountRoot string
}

// IgnoredRung is one rung whose binding the provenance gate ignored.
type IgnoredRung struct {
	Path   string // the binding file
	Reason string // why, with the remedy — never the file's contents
}

// IgnoredRungs lists the rungs Lock 1 ignored, nearest first.
func (r Resolution) IgnoredRungs() []IgnoredRung {
	if r.IgnoredNote == "" {
		return nil
	}
	var out []IgnoredRung
	for _, line := range strings.Split(r.IgnoredNote, "\n") {
		p, why, _ := strings.Cut(line, "\t")
		out = append(out, IgnoredRung{Path: p, Reason: why})
	}
	return out
}

// Resolve resolves projectPath on both axes. See the file header for the walk
// and its safety properties. "" short-circuits to Source "none" with NO walk:
// a relative binding path would otherwise be read against the process's own
// working directory.
func Resolve(projectPath string) Resolution {
	if strings.TrimSpace(projectPath) == "" {
		return Resolution{Source: SourceNone, DefaultProfile: true}
	}
	path := cleanAbs(projectPath)
	walkFrom, cands, nss, ignored := candidates(path)

	r := Resolution{Source: SourceDefault}
	var notes []string
	for i, why := range ignored {
		if why != "" {
			notes = append(notes, bindingPath(cands[i])+"\t"+why)
		}
	}
	r.IgnoredNote = strings.Join(notes, "\n")
	// Scan 1 — the ACCOUNT: the first candidate whose claudeAccount is valid.
	for i, ns := range nss {
		if key := validField(ns, bindingField); key != "" {
			r.Account = key
			r.AccountRoot = cands[i]
			r.Source = SourcePinParent
			if i == 0 {
				r.Source = SourcePin
			}
			break
		}
	}
	// Scan 2 — the ESTATE: the first candidate whose estate is valid, whatever
	// rung scan 1 stopped at.
	for i, ns := range nss {
		if key := validField(ns, estateField); key != "" {
			r.Estate = key
			r.EstateRoot = cands[i]
			break
		}
	}

	r.setAccountDerived()
	r.applyAdmission()
	if r.Estate != "" {
		// Only an ADMITTED estate contributes its settings file: an estate its
		// store does not anchor is unanchored — zero credentials AND no settings.
		if f := filepath.Join(r.EstateRoot, filepath.FromSlash(ProjectSettingsFile)); r.EstateAdmitted && isRegularFile(f) {
			r.SettingsFile = f
			// Against the path the walk ran from: for a daemon worktree that is
			// its SOURCE checkout, whose settings file is the project's own.
			r.EstateSettingsIsProjects = SameFile(f, filepath.Join(walkFrom, filepath.FromSlash(ProjectSettingsFile)))
		}
		// Lstat, not Stat: a symlinked or otherwise refused store must still be
		// SEEN here, so CredentialStore can report it as refused rather than absent.
		if p := SecretsPath(r.Estate); p != "" {
			if _, err := os.Lstat(p); err == nil {
				r.envPath = p
			}
		}
	}
	return r
}

// applyAdmission fills the admission fields from storeroot.go's release table.
func (r *Resolution) applyAdmission() {
	a := admit(*r)
	r.AccountStoreAdmitted = a.accountReleased
	r.EstateAdmitted = a.estateReleased
	var notes []string
	for _, n := range []string{a.accountNote, a.estateNote} {
		if n != "" {
			notes = append(notes, n)
		}
	}
	r.AdmissionNote = strings.Join(notes, "\n")
}

// WithAccount forces the payer and keeps the estate: the resolution a spawn
// needs when the ACCOUNT is already decided elsewhere (a resumed session must
// run under the config dir that wrote its transcript) but the ESTATE still has
// to come from the directory. Source becomes SourceForced and AccountRoot "".
//
// What the directory itself resolved the account to is KEPT: the forced rule
// releases the account's store only when the directory independently resolves
// the same key under a rung that store admits (storeroot.go). A foreign binding
// that re-homed a session onto an account therefore never unlocks that
// account's store by being resumed.
func (r Resolution) WithAccount(key string) Resolution {
	if r.Source != SourceForced {
		r.cwdAccount, r.cwdAccountRoot = r.Account, r.AccountRoot
	}
	r.Account = strings.TrimSpace(key)
	r.Source = SourceForced
	r.AccountRoot = ""
	r.setAccountDerived()
	r.applyAdmission()
	return r
}

// EnvLines is the CONFIG-DIR delta for the resolved account: zero lines for an
// unresolved or default account, one "CLAUDE_CONFIG_DIR=<dir>" line otherwise.
// It never carries a credential — this is what `swarmery account env` prints.
func (r Resolution) EnvLines() []string {
	return EnvForAccount(r.Account)
}

// CredentialCount is the number of variable NAMES the estate's credential store
// supplies — 0 for no estate and for an estate with no store, both healthy. A
// count is the only thing about a store a surface may print.
func (r Resolution) CredentialCount() int {
	if r.envPath == "" || !r.EstateAdmitted {
		return 0
	}
	return len(secretEnvFromFile(r.envPath))
}

// CredentialStore is the estate's store as the LOADER sees it: StoreAbsent (no
// estate, or no file — healthy), StorePresent (it will be read), or StoreRefused
// with the reason (a symlink, a mode or owner the loader rejects, an open store
// directory). The verdict comes from the loader's own checks (openStore), so a
// surface can never call "present" a store no spawn will read. The reason names
// neither the store's path nor anything in it; nothing is logged.
//
// A store the loader would read but whose roots do not admit the estate root —
// or that carries no root line at all — is StoreUnadmitted, with the
// admission line as its reason: it supplies zero credentials, and the operator
// has a line to add, not a file to repair.
func (r Resolution) CredentialStore() (state StoreState, reason string) {
	if r.envPath == "" {
		return StoreAbsent, ""
	}
	f, c := openStore(r.envPath)
	if f != nil {
		f.Close()
	}
	if c.State == StorePresent && !r.EstateAdmitted {
		sr := readStoreRoots(r.envPath)
		if sr.rooted {
			return StoreUnadmitted, notAdmittedLine(r.Estate)
		}
		return StoreUnadmitted, unanchoredLine(r.Estate) + ": " + r.Estate + ".env carries no root line"
	}
	return c.State, c.Reason
}

// HasCredentialStore reports whether the estate has a store the loader will
// read — so a surface can say "no store" apart from "a store with no names",
// without ever printing the store's path. A REFUSED store is not "has": see
// CredentialStore for the three-way answer.
func (r Resolution) HasCredentialStore() bool {
	state, _ := r.CredentialStore()
	return state == StorePresent
}

// AncestorPin is one ancestor binding: the directory and the account it pins.
type AncestorPin struct {
	Dir     string
	Account string
}

// Shadowed lists the ancestors ABOVE the rung that decided projectPath's
// account whose own pin names a DIFFERENT account — nearest first. They are what
// the winning pin shadows, and a surface reports them so an inherited answer is
// never silent. Empty when nothing pinned the account (there is then no pin
// anywhere on the ladder to shadow) and for "".
func Shadowed(projectPath string) []AncestorPin {
	if strings.TrimSpace(projectPath) == "" {
		return nil
	}
	_, cands, nss, _ := candidates(cleanAbs(projectPath))
	won := -1
	var winner string
	for i, ns := range nss {
		if key := validField(ns, bindingField); key != "" {
			won, winner = i, key
			break
		}
	}
	if won < 0 {
		return nil
	}
	var out []AncestorPin
	for i := won + 1; i < len(nss); i++ {
		if key := validField(nss[i], bindingField); key != "" && key != winner {
			out = append(out, AncestorPin{Dir: cands[i], Account: key})
		}
	}
	return out
}

// candidates is the ladder for path — walked from its source checkout when path
// is under the daemon's worktree root — and each rung's `swarmery` namespace
// (nil where nothing is declared or the file is unreadable), and for each rung
// the reason Lock 1 ignored its binding ("" where it did not). walkFrom is the
// path the ladder starts at.
func candidates(path string) (walkFrom string, cands []string, nss []map[string]any, ignored []string) {
	walkFrom = path
	if src := sourceCheckout(path); src != "" {
		walkFrom = src
	}
	cands = ladder(walkFrom)
	nss = make([]map[string]any, len(cands))
	ignored = make([]string, len(cands))
	for i, dir := range cands {
		nss[i], ignored[i] = namespaceAt(dir)
	}
	return walkFrom, cands, nss, ignored
}

// DeclarationUnreadable says why a declaration written into dir's binding file
// would NEVER be read back by Resolve(dir) as dir's own rung — "" when it would.
// A writer (`swarmery account estate use`) checks this BEFORE writing, so it
// never leaves a declaration on disk that nothing reads: dir inside a daemon
// worktree (Resolve walks its source checkout instead), dir == $HOME or the
// filesystem root (never a candidate), dir not owned by the current user (the
// walk stops below it), or an existing binding file the trust checks ignore.
func DeclarationUnreadable(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "no directory was given"
	}
	path := cleanAbs(dir)
	if src := sourceCheckout(path); src != "" {
		return fmt.Sprintf("%s is inside a daemon worktree, which resolves from its source checkout %s", path, src)
	}
	if lad := ladder(path); len(lad) == 0 || lad[0] != path {
		if h, err := userHomeDir(); err == nil && strings.TrimSpace(h) != "" && filepath.Clean(h) == path {
			return fmt.Sprintf("%s is the home directory, which the walk never reads", path)
		}
		return fmt.Sprintf("%s is not a rung the walk reads (the filesystem root, or a directory not owned by you)", path)
	}
	return bindingUntrusted(bindingPath(path))
}

// setAccountDerived recomputes the fields that follow from Account.
func (r *Resolution) setAccountDerived() {
	r.DefaultProfile = r.Account == "" || r.Account == ingest.DefaultAccount
	r.ConfigDir = ""
	if !r.DefaultProfile {
		if dir, ok := ConfigDirForAccount(r.Account); ok {
			r.ConfigDir = dir
		}
	}
}

// SameFile reports whether a and b name the same file, by RESOLVED absolute
// path: filepath.EvalSymlinks on both sides, results compared. Where either side
// cannot be resolved (missing, dangling link, permission error) the two are
// treated as DIFFERENT — a duplicate contribution is recoverable, a wrongly
// dropped one is not. A string compare is never used: a symlinked .claude
// directory spells one file two ways.
func SameFile(a, b string) bool {
	ra, err := filepath.EvalSymlinks(cleanAbs(a))
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(cleanAbs(b))
	if err != nil {
		return false
	}
	return ra == rb
}

// ladder is the ordered candidate list for path: path itself, then each
// ancestor, stopping BEFORE $HOME, never including the filesystem root, capped
// at maxLadderRungs, and stopping at the first existing directory not owned by
// the current user (that directory is not a candidate either).
func ladder(path string) []string {
	home := ""
	if h, err := userHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		home = filepath.Clean(h)
	}
	var out []string
	dir := path
	for i := 0; i < maxLadderRungs; i++ {
		if home != "" && dir == home {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // the filesystem root: never a candidate
		}
		if !ownRung(dir) {
			break
		}
		out = append(out, dir)
		dir = parent
	}
	return out
}

// ownRung reports whether dir may be a rung: it is owned by the current user, or
// it does not exist (then nothing can be read from it and the walk goes on up —
// a not-yet-created project path still inherits its ancestors). Any other stat
// failure stops the walk: what cannot be inspected is not trusted.
func ownRung(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
	}
	uid, ok := fileOwner(fi)
	return ok && uid == currentUID()
}

// namespaceAt returns the `swarmery` object of dir's binding file, or nil for a
// missing, unparseable, untrusted (readTrustedSettings) or namespace-less file —
// all of which mean "nothing declared here", never an error.
//
// Lock 1 runs HERE, so every reader of a rung — both Resolve scans, Shadowed,
// Estate — sees only provenance-clean bindings: a file that carries a
// `swarmery` object and that git tracks (or whose status git cannot tell) reads
// as nothing declared, and ignored says why. The probe runs only for a file
// that exists and carries the namespace — never for a missing file, and never
// on a relative path (dir is always an absolute ladder rung). It logs nothing:
// the WARN is the composer's (spawnenv.go), once per path per process.
func namespaceAt(dir string) (ns map[string]any, ignored string) {
	path := bindingPath(dir)
	root := readTrustedSettings(path)
	if root == nil {
		return nil, ""
	}
	ns, _ = root[bindingNamespace].(map[string]any)
	if ns == nil {
		return nil, ""
	}
	if why := bindingIgnoredReason(path); why != "" {
		return nil, why
	}
	return ns, ""
}

// validField is one string field of a namespace, trimmed and gated by ValidKey
// BEFORE it can reach any path join. "" when absent or invalid.
func validField(ns map[string]any, field string) string {
	if ns == nil {
		return ""
	}
	v, _ := ns[field].(string)
	v = strings.TrimSpace(v)
	if !ValidKey(v) {
		return ""
	}
	return v
}

// cleanAbs is filepath.Abs with the error folded into a Clean: resolution never
// fails, and Abs only errors when the working directory is unreadable.
func cleanAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
