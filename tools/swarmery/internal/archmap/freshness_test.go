package archmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	headC = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeGit writes a loose-ref .git at dir whose HEAD resolves to sha. It is the
// same layout githead reads in production, without forking git.
func fakeGit(t *testing.T, dir, sha string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(gitDir, "refs", "heads", "main"), sha+"\n")
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeMapMeta writes an architecture-map.json carrying only the freshness
// stamps — everything else in the artifact is irrelevant to ResolveFreshness,
// and writing a full map would hide that it does not go through Load.
func writeMapMeta(t *testing.T, root, single string, per map[string]string) {
	t.Helper()
	meta := map[string]any{}
	if single != "" {
		meta["analyzedAtCommit"] = single
	}
	if per != nil {
		meta["analyzedAtCommits"] = per
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, OutDir, MapFileName), string(raw))
}

// writeProjectJSON writes .claude/project.json declaring member repos.
func writeProjectJSON(t *testing.T, root string, repos []string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"name": "fixture", "repos": repos})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, ".claude", "project.json"), string(raw))
}

func TestResolveFreshnessSingleRepo(t *testing.T) {
	t.Run("root .git resolves to Single, Repos stays empty", func(t *testing.T) {
		root := t.TempDir()
		fakeGit(t, root, headA)
		writeMapMeta(t, root, headB, nil)
		// A repos[] list must be IGNORED once the root is itself a checkout:
		// otherwise a single-repo project that happens to declare searchable
		// subdirs would silently change wire shape.
		writeProjectJSON(t, root, []string{"sub-a", "sub-b"})

		f := ResolveFreshness(root)
		if f.Single == nil {
			t.Fatal("Single = nil, want the root HEAD")
		}
		if *f.Single != headA {
			t.Errorf("Single = %q, want %q", *f.Single, headA)
		}
		if len(f.Repos) != 0 {
			t.Errorf("Repos = %+v, want empty for a single-repo project", f.Repos)
		}
		if f.MultiRepo() {
			t.Error("MultiRepo() = true, want false")
		}
		if !f.Comparable || !f.Stale {
			t.Errorf("Comparable/Stale = %v/%v, want true/true (HEAD %s ≠ analyzed %s)", f.Comparable, f.Stale, headA[:7], headB[:7])
		}
		if f.Analyzed != headB {
			t.Errorf("Analyzed = %q, want %q", f.Analyzed, headB)
		}
	})

	t.Run("analyzed == HEAD is current, not stale", func(t *testing.T) {
		root := t.TempDir()
		fakeGit(t, root, headA)
		writeMapMeta(t, root, headA, nil)

		f := ResolveFreshness(root)
		if !f.Comparable {
			t.Fatal("Comparable = false, want true")
		}
		if f.Stale {
			t.Error("Stale = true, want false when analyzed == HEAD")
		}
	})

	t.Run("unreadable .git yields no answer, never a guess", func(t *testing.T) {
		root := t.TempDir()
		// A .git that exists but holds nothing githead can resolve.
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeMapMeta(t, root, headB, nil)
		// Even with repos[] declared, a broken single repo must NOT fall
		// through to the multi-repo branch.
		writeProjectJSON(t, root, []string{"sub-a"})

		f := ResolveFreshness(root)
		if f.Single != nil {
			t.Errorf("Single = %q, want nil for an unreadable .git", *f.Single)
		}
		if len(f.Repos) != 0 {
			t.Errorf("Repos = %+v, want empty — a corrupt .git is a broken single repo, not a workspace", f.Repos)
		}
		if f.Comparable || f.Stale {
			t.Errorf("Comparable/Stale = %v/%v, want false/false", f.Comparable, f.Stale)
		}
	})

	t.Run("no map means no comparison", func(t *testing.T) {
		root := t.TempDir()
		fakeGit(t, root, headA)

		f := ResolveFreshness(root)
		if f.Single == nil || *f.Single != headA {
			t.Fatalf("Single = %v, want %q", f.Single, headA)
		}
		if f.Comparable || f.Stale {
			t.Errorf("Comparable/Stale = %v/%v, want false/false with no map on disk", f.Comparable, f.Stale)
		}
	})
}

func TestResolveFreshnessMultiRepoMissingMember(t *testing.T) {
	root := t.TempDir()
	// No root .git — this is the shape that used to answer "unknown".
	writeProjectJSON(t, root, []string{"repo-one", "repo-two", "repo-gone"})
	fakeGit(t, filepath.Join(root, "repo-one"), headA)
	fakeGit(t, filepath.Join(root, "repo-two"), headB)
	// repo-gone is declared but never created on disk.
	writeMapMeta(t, root, "", map[string]string{
		"repo-one":  headA, // current
		"repo-two":  headC, // moved past the map
		"repo-gone": headA,
	})

	f := ResolveFreshness(root)
	if !f.MultiRepo() {
		t.Fatal("MultiRepo() = false, want true")
	}
	if f.Single != nil {
		t.Errorf("Single = %q, want nil for a rootless workspace", *f.Single)
	}
	if len(f.Repos) != 3 {
		t.Fatalf("Repos = %d entries, want 3 — a missing member must be REPORTED, not dropped (a dropped member reads as 'everything is fine')", len(f.Repos))
	}
	if f.Repos[0].Name != "repo-one" || f.Repos[1].Name != "repo-two" || f.Repos[2].Name != "repo-gone" {
		t.Errorf("repo order = %q/%q/%q, want project.json declaration order", f.Repos[0].Name, f.Repos[1].Name, f.Repos[2].Name)
	}

	gone := f.Repos[2]
	if gone.OK {
		t.Error("repo-gone OK = true, want false for a member that is not on disk")
	}
	if gone.Head != "" {
		t.Errorf("repo-gone Head = %q, want empty", gone.Head)
	}
	if gone.Measurable() || gone.Stale() {
		t.Error("repo-gone must be neither measurable nor stale — unknown is not stale")
	}

	if got := f.ResolvedRepos(); got != 2 {
		t.Errorf("ResolvedRepos() = %d, want 2", got)
	}
	if !f.Comparable {
		t.Error("Comparable = false, want true — two members had both halves known")
	}
	if !f.Stale {
		t.Error("Stale = false, want true — repo-two moved past the map")
	}
	if got := f.StaleRepos(); got != 1 {
		t.Errorf("StaleRepos() = %d, want 1 (only repo-two moved)", got)
	}
	if f.Repos[0].Stale() {
		t.Error("repo-one Stale() = true, want false — its HEAD equals the recorded commit")
	}
	if f.Repos[0].Path != filepath.Join(root, "repo-one") {
		t.Errorf("repo-one Path = %q, want %q", f.Repos[0].Path, filepath.Join(root, "repo-one"))
	}
}

func TestResolveFreshnessMultiRepoScalarStampIsUnknownNotFresh(t *testing.T) {
	root := t.TempDir()
	writeProjectJSON(t, root, []string{"repo-one", "repo-two"})
	fakeGit(t, filepath.Join(root, "repo-one"), headA)
	fakeGit(t, filepath.Join(root, "repo-two"), headB)
	// The shape every multi-repo map has today: ONE commit, no way to say
	// which repo it belongs to.
	writeMapMeta(t, root, "1097a7f", nil)

	f := ResolveFreshness(root)
	if len(f.Repos) != 2 {
		t.Fatalf("Repos = %d, want 2", len(f.Repos))
	}
	if f.Analyzed != "1097a7f" {
		t.Errorf("Analyzed = %q, want the scalar stamp", f.Analyzed)
	}
	for _, r := range f.Repos {
		if r.Analyzed != "" {
			t.Errorf("%s Analyzed = %q, want empty — a scalar stamp says nothing per repo", r.Name, r.Analyzed)
		}
	}
	if f.Comparable {
		t.Error("Comparable = true, want false — nothing could be compared")
	}
	if f.Stale {
		t.Error("Stale = true, want false — staleness here is decided by age, not by a guess")
	}
	if got := f.ResolvedRepos(); got != 2 {
		t.Errorf("ResolvedRepos() = %d, want 2", got)
	}
}

func TestResolveFreshnessNoReposAtAll(t *testing.T) {
	t.Run("no .git and no project.json", func(t *testing.T) {
		root := t.TempDir()
		writeMapMeta(t, root, headB, nil)

		f := ResolveFreshness(root)
		if f.Single != nil {
			t.Errorf("Single = %q, want nil", *f.Single)
		}
		if len(f.Repos) != 0 {
			t.Errorf("Repos = %+v, want empty", f.Repos)
		}
		if f.Comparable || f.Stale || f.MultiRepo() {
			t.Errorf("Comparable/Stale/MultiRepo = %v/%v/%v, want all false", f.Comparable, f.Stale, f.MultiRepo())
		}
	})

	t.Run("project.json with an empty repos list", func(t *testing.T) {
		root := t.TempDir()
		writeProjectJSON(t, root, []string{})

		f := ResolveFreshness(root)
		if len(f.Repos) != 0 {
			t.Errorf("Repos = %+v, want empty", f.Repos)
		}
	})

	t.Run("unparseable project.json", func(t *testing.T) {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, ".claude", "project.json"), "not json at all {{")

		f := ResolveFreshness(root)
		if len(f.Repos) != 0 {
			t.Errorf("Repos = %+v, want empty for an unparseable project.json", f.Repos)
		}
	})
}

func TestResolveFreshnessRejectsEscapingRepoEntries(t *testing.T) {
	root := t.TempDir()
	// A sibling checkout the project never claimed.
	fakeGit(t, filepath.Join(root, "outside"), headA)
	inner := filepath.Join(root, "inner")
	writeProjectJSON(t, inner, []string{"../outside", "/etc", "", ".", "ok-repo", "ok-repo"})
	fakeGit(t, filepath.Join(inner, "ok-repo"), headB)

	f := ResolveFreshness(inner)
	if len(f.Repos) != 1 {
		t.Fatalf("Repos = %+v, want only the one safe, de-duplicated entry", f.Repos)
	}
	if f.Repos[0].Name != "ok-repo" {
		t.Errorf("Repos[0].Name = %q, want ok-repo", f.Repos[0].Name)
	}
}

func TestResolveFreshnessIgnoresShortStamps(t *testing.T) {
	root := t.TempDir()
	fakeGit(t, root, headA)
	writeMapMeta(t, root, "abc", nil) // below the schema's 7-char floor

	f := ResolveFreshness(root)
	if f.Analyzed != "" {
		t.Errorf("Analyzed = %q, want empty — a 3-char stamp is a typo, not a commit", f.Analyzed)
	}
	if f.Comparable || f.Stale {
		t.Errorf("Comparable/Stale = %v/%v, want false/false", f.Comparable, f.Stale)
	}
}

func TestResolveFreshnessUnparseableMap(t *testing.T) {
	root := t.TempDir()
	fakeGit(t, root, headA)
	writeTestFile(t, filepath.Join(root, OutDir, MapFileName), "not valid JSON!!!")

	f := ResolveFreshness(root)
	if f.Single == nil || *f.Single != headA {
		t.Fatalf("Single = %v, want %q — a broken map must not cost us the HEAD", f.Single, headA)
	}
	if f.Analyzed != "" || f.Comparable || f.Stale {
		t.Errorf("Analyzed/Comparable/Stale = %q/%v/%v, want empty/false/false", f.Analyzed, f.Comparable, f.Stale)
	}
}
