package worktree

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
)

// withMemoryHome points the memory helpers at a throwaway home for the length
// of one test. Every test that touches the filesystem goes through it, so no
// test in this file can reach the operator's real `~/.claude`.
func withMemoryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := memoryHome
	memoryHome = home
	t.Cleanup(func() { memoryHome = prev })
	return home
}

// slugDir is `<home>/.claude/projects/<ProjectSlug(cwd)>`.
func slugDir(home, cwd string) string {
	return filepath.Join(home, ".claude", "projects", ProjectSlug(cwd))
}

// TestProjectSlugEncoding asserts DELEGATION, not the rule.
//
// ProjectSlug is a one-line alias for claudeproj.Slug, so a table mirrored here
// would be a second copy of one encoding, hand-kept in sync — which is the very
// drift this package's alias exists to prevent. The rule's own table, with the
// binary-derived evidence behind every row, lives in
// internal/claudeproj's TestSlug.
func TestProjectSlugEncoding(t *testing.T) {
	paths := []string{
		"/Volumes/Work/swarmery",
		"/Volumes/Work/swarmery/tools/swarmery",
		"/Users/dev",
		"/Users/dev/.swarmery/worktrees/-Volumes-Work-swarmery/plan-530",
		"/Users/dev/src/my_app+v2",
		"/Users/dev/src/app/api/missions/[id]",
		"/Volumes/Work/swarmery/",
		"/",
		"",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			if got, want := ProjectSlug(p), claudeproj.Slug(p); got != want {
				t.Fatalf("ProjectSlug(%q) = %q but claudeproj.Slug(%q) = %q — "+
					"ProjectSlug must stay a delegation, never a second implementation", p, got, p, want)
			}
		})
	}
}

// TestProjectSlugMatchesRealClaudeProjectDirs is the encoding's ground truth:
// it re-encodes every `cwd` and `relocatedCwd` a project's transcripts record
// and requires one of them to reproduce the directory name Claude Code itself
// chose. Read-only, and skipped wherever there are no transcripts (CI, a fresh
// machine), so it strengthens the local signal without becoming a portability
// trap. internal/claudeproj's TestSlug is the hermetic half that still goes red
// on a machine where this one skips.
//
// Three things here are deliberate and were each a defect once:
//
//   - It globs `<home>/.claude*/projects`, not `<home>/.claude/projects`. An
//     operator running several accounts keeps one config dir per account, and
//     reading only the first excluded a whole account's projects from the
//     ground truth. The IsDir guard is what rejects the `.claude.json` and
//     friends the same glob matches.
//   - It reads EVERY transcript and collects EVERY recorded cwd, rather than
//     the first match in the first file. Claude Code's own worktree feature
//     relocates a session's cwd and re-homes its transcript while earlier lines
//     keep the old path, so the cwd that names the directory is often neither
//     the first one recorded nor the last.
//   - A directory passes when ANY candidate re-encodes to its name, never when
//     every one does. A transcript's recorded cwd set is a SUPERSET of the
//     paths that name directories — a dynamic-route path such as
//     `…/api/missions/[id]` is recorded as a cwd and names no directory under
//     any config dir — so an "every" variant would be red on arrival.
func TestProjectSlugMatchesRealClaudeProjectDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	candidates, err := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
	if err != nil {
		t.Skipf("glob %s: %v", filepath.Join(home, ".claude*", "projects"), err)
	}
	var roots []string
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			roots = append(roots, c)
		}
	}
	if len(roots) == 0 {
		t.Skipf("no <home>/.claude*/projects on this machine")
	}

	// Both keys, because a relocated session records the new cwd under
	// `relocatedCwd` while `cwd` keeps naming the original checkout.
	cwdRe := regexp.MustCompile(`"(?:cwd|relocatedCwd)":"((?:[^"\\]|\\.)*)"`)

	reproduced := 0
	var (
		skipNoTranscripts int
		skipUnreadable    int
		skipNoCwdMatched  int
	)
	for _, projects := range roots {
		entries, err := os.ReadDir(projects)
		if err != nil {
			t.Errorf("read %s: %v", projects, err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			transcripts, err := filepath.Glob(filepath.Join(projects, e.Name(), "*.jsonl"))
			if err != nil || len(transcripts) == 0 {
				skipNoTranscripts++
				continue
			}

			var (
				seen      = map[string]bool{}
				order     []string
				matched   bool
				unreadabl bool
			)
			for _, tp := range transcripts {
				f, err := os.Open(tp)
				if err != nil {
					unreadabl = true
					continue
				}
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
				for sc.Scan() {
					for _, m := range cwdRe.FindAllStringSubmatch(sc.Text(), -1) {
						cwd := m[1]
						if unquoted, err := strconv.Unquote(`"` + cwd + `"`); err == nil {
							cwd = unquoted
						}
						if cwd == "" || seen[cwd] {
							continue
						}
						seen[cwd] = true
						order = append(order, cwd)
						if ProjectSlug(cwd) == e.Name() {
							matched = true
						}
					}
				}
				if err := sc.Err(); err != nil {
					unreadabl = true
				}
				f.Close()
				if matched {
					// The first transcript that settles the directory is
					// enough; the rest is 2 GB of I/O for no more evidence.
					break
				}
			}

			switch {
			case matched:
				// Counted AFTER the comparison. Counting before it is how the
				// old log reported more reproductions than it had.
				reproduced++
			case len(order) == 0 && unreadabl:
				skipUnreadable++
			case len(order) == 0:
				skipNoCwdMatched++
			default:
				t.Errorf("no recorded cwd re-encodes to %q (in %s): %d candidate(s) %q; "+
					"ProjectSlug(%q) = %q", e.Name(), projects, len(order), order, order[0], ProjectSlug(order[0]))
			}
		}
	}
	if reproduced == 0 {
		t.Skip("no transcript recorded a cwd to check the encoding against")
	}
	t.Logf("encoding reproduced %d real project slug directories across %d config-dir root(s) %q "+
		"(skipped: %d with no transcript, %d unreadable, %d with no cwd recorded)",
		reproduced, len(roots), roots, skipNoTranscripts, skipUnreadable, skipNoCwdMatched)
}

// TestMemoryLinkDisabledByProbe pins the probe's verdict. If someone flips the
// const they must also wire the call sites the package doc names, and this test
// is where that conversation starts.
func TestMemoryLinkDisabledByProbe(t *testing.T) {
	if linkMemory {
		t.Fatal("linkMemory is true, but LinkMemory/UnlinkMemory are still uncalled — " +
			"wire Manager.Acquire, Manager.Remove and the wtjanitor removal path, " +
			"or set it back to false (see memory.go)")
	}
}

func TestMemoryDirRefusesRelativePaths(t *testing.T) {
	withMemoryHome(t)
	if _, err := MemoryDir("tools/swarmery"); err == nil {
		t.Fatal("MemoryDir accepted a relative path")
	}
}

func TestLinkMemoryCreatesAndIsIdempotent(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	canonicalMemory := filepath.Join(slugDir(home, canonical), "memory")
	if err := os.MkdirAll(canonicalMemory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalMemory, "MEMORY.md"), []byte("- index\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(slugDir(home, worktree), "memory")
	for i := range 2 { // twice: the second call must be a no-op, not an error
		if err := LinkMemory(canonical, worktree); err != nil {
			t.Fatalf("LinkMemory call %d: %v", i+1, err)
		}
		dest, err := os.Readlink(target)
		if err != nil {
			t.Fatalf("after call %d: %v", i+1, err)
		}
		if dest != canonicalMemory {
			t.Fatalf("after call %d: link points at %q, want %q", i+1, dest, canonicalMemory)
		}
		got, err := os.ReadFile(filepath.Join(target, "MEMORY.md"))
		if err != nil {
			t.Fatalf("after call %d: reading through the link: %v", i+1, err)
		}
		if string(got) != "- index\n" {
			t.Fatalf("after call %d: read %q through the link", i+1, got)
		}
	}
}

func TestLinkMemoryNoOpWhenCanonicalMemoryMissing(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	// The canonical project exists as a slug directory but has never built any
	// memory — the normal state of a fresh project.
	if err := os.MkdirAll(slugDir(home, canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LinkMemory(canonical, worktree); err != nil {
		t.Fatalf("a missing canonical memory must not be an error: %v", err)
	}
	if _, err := os.Lstat(slugDir(home, worktree)); !os.IsNotExist(err) {
		t.Fatal("LinkMemory created a worktree slug directory with nothing to put in it")
	}
}

func TestLinkMemoryNoOpWhenCanonicalProjectAbsent(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	if err := LinkMemory(canonical, worktree); err != nil {
		t.Fatalf("an unknown canonical project must not be an error: %v", err)
	}
	if _, err := os.Lstat(slugDir(home, worktree)); !os.IsNotExist(err) {
		t.Fatal("LinkMemory created a worktree slug directory for an unknown project")
	}
}

// TestLinkMemorySurfacesAnUnreadableCanonicalPath is F3's regression: only
// ACTUAL absence may be reported as "no auto-memory directory yet". Here the
// canonical project's slug path is a regular file, so stat'ing `…/memory`
// underneath it fails with ENOTDIR — a stat error that is not ENOENT, the same
// class as EACCES or ELOOP. Reporting that as absence would return nil and
// leave the worktree silently unlinked under a log line naming the wrong cause.
func TestLinkMemorySurfacesAnUnreadableCanonicalPath(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	dir := slugDir(home, canonical)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := LinkMemory(canonical, worktree)
	if err == nil {
		t.Fatal("an unreadable canonical path was swallowed as 'no auto-memory yet'")
	}
	if os.IsNotExist(err) {
		t.Fatalf("err = %v, want a wrapped non-ENOENT stat error", err)
	}
	if !strings.Contains(err.Error(), "inspect the canonical memory") {
		t.Fatalf("err = %v, want it to name the real cause", err)
	}
}

func TestLinkMemoryRefusesToClobberARealDirectory(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	if err := os.MkdirAll(filepath.Join(slugDir(home, canonical), "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(slugDir(home, worktree), "memory")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "MEMORY.md")
	if err := os.WriteFile(keep, []byte("real memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := LinkMemory(canonical, worktree)
	if !errors.Is(err, ErrMemoryTargetNotSymlink) {
		t.Fatalf("err = %v, want ErrMemoryTargetNotSymlink", err)
	}
	got, readErr := os.ReadFile(keep)
	if readErr != nil || string(got) != "real memory\n" {
		t.Fatalf("the real directory was disturbed: %q, %v", got, readErr)
	}
}

func TestLinkMemoryRepointsAStaleLink(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	canonicalMemory := filepath.Join(slugDir(home, canonical), "memory")
	if err := os.MkdirAll(canonicalMemory, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(home, "somewhere-else")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(slugDir(home, worktree), "memory")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stale, target); err != nil {
		t.Fatal(err)
	}

	if err := LinkMemory(canonical, worktree); err != nil {
		t.Fatal(err)
	}
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if dest != canonicalMemory {
		t.Fatalf("link still points at %q, want %q", dest, canonicalMemory)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("repointing a link must not touch its old target: %v", err)
	}
}

func TestLinkMemorySelfIsANoOp(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"

	canonicalMemory := filepath.Join(slugDir(home, canonical), "memory")
	if err := os.MkdirAll(canonicalMemory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LinkMemory(canonical, canonical); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(canonicalMemory)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("LinkMemory replaced a project's own memory directory with a symlink to itself")
	}
}

func TestUnlinkMemoryRemovesOnlyItsOwnLink(t *testing.T) {
	home := withMemoryHome(t)
	const canonical = "/Volumes/Work/repo"
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	if err := os.MkdirAll(filepath.Join(slugDir(home, canonical), "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LinkMemory(canonical, worktree); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(slugDir(home, worktree), "memory")
	if err := UnlinkMemory(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the link survived UnlinkMemory: %v", err)
	}
	// Idempotent: a second reap of the same worktree is not an error.
	if err := UnlinkMemory(worktree); err != nil {
		t.Fatalf("UnlinkMemory on an absent link: %v", err)
	}
	// The canonical memory the link pointed at is untouched.
	if _, err := os.Stat(filepath.Join(slugDir(home, canonical), "memory")); err != nil {
		t.Fatalf("UnlinkMemory followed the link and removed the real memory: %v", err)
	}
}

func TestUnlinkMemoryRefusesARealDirectory(t *testing.T) {
	home := withMemoryHome(t)
	const worktree = "/Users/dev/.swarmery/worktrees/-Volumes-Work-repo/plan-1"

	target := filepath.Join(slugDir(home, worktree), "memory")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "MEMORY.md")
	if err := os.WriteFile(keep, []byte("real memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := UnlinkMemory(worktree)
	if !errors.Is(err, ErrMemoryTargetNotSymlink) {
		t.Fatalf("err = %v, want ErrMemoryTargetNotSymlink", err)
	}
	if _, statErr := os.Stat(keep); statErr != nil {
		t.Fatalf("UnlinkMemory deleted a real memory directory: %v", statErr)
	}
}

// TestMemoryHelpersStayDormant is the structural half of the finding: the
// package documents that nothing calls these helpers, and a reader should be
// able to trust that without grepping. If a call site appears, either the probe
// flipped (update the doc and the const) or it crept in by accident.
//
// It scans the whole of `internal/`, not just this package. The helpers are
// EXPORTED, and the flip the package doc describes wires them from
// `internal/wtjanitor` and `Manager.Acquire` — a same-package scan would stay
// green through exactly the change it exists to notice, while its claim
// ("nothing calls them") quietly became false.
func TestMemoryHelpersStayDormant(t *testing.T) {
	// Package-local calls are unqualified; calls from any other package are
	// qualified with the package name. Both shapes have to be caught.
	local := []string{"LinkMemory(", "UnlinkMemory("}
	qualified := []string{"worktree.LinkMemory(", "worktree.UnlinkMemory("}

	thisPackage := filepath.Join("..", "worktree")
	// internal/ holds every named flip target (wtjanitor, dispatch, the manager
	// itself); cmd/ is the only other place module code lives. web/ and
	// node_modules are deliberately not walked — no Go there.
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// memory.go declares them; every other file in this package would
			// be a real call site.
			inThisPackage := filepath.Dir(path) == thisPackage
			if inThisPackage && filepath.Base(path) == "memory.go" {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			needles := qualified
			if inThisPackage {
				needles = local
			}
			for _, fn := range needles {
				if strings.Contains(string(raw), fn) {
					t.Errorf("%s calls %s — the helpers are documented as dormant (linkMemory=false); "+
						"re-run scripts/tests/worktree-memory-probe.sh and update memory.go's finding", path, fn)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
