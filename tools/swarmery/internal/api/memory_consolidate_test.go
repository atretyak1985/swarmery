package api

// agent-memory phase 3: POST /api/memory/consolidate.
//
// The two properties this endpoint lives or dies by:
//   - a DRY RUN leaves the auto-memory directory byte-identical (the UI runs one
//     on every page load; it must be free of consequence);
//   - an APPLY takes a backup covering every touched file BEFORE the first write.
//
// Plus the fence: the ?path= handle must BE some registered project's
// auto-memory root, never an arbitrary directory, a `../` walk, or one of the
// other two memory roots.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysedit"
)

// consolidateIndex is the fixture index: 4 entries, 2 of them consolidatable
// (one closed entry is held back by the [[link]] guard).
const consolidateIndex = `- [Order line items](order-line-items.md) — 7-phase plan; impl open.
- [Search indexer](search-indexer.md) — DONE 2026-07-27: slugs name-derived.
- [Billing migration](billing-migration.md) — MERGED 2026-08-10 + deployed; DONE.
- [Inventory audit](inventory-audit.md) — SHIPPED 2026-08-11; tail = deploy.
`

// seedConsolidateFixture fills the fixture's auto-memory dir with an index and
// its topic files. order-line-items is OPEN and links to billing-migration, so
// the dependency guard holds billing-migration back and only search-indexer moves.
func seedConsolidateFixture(t *testing.T, fx *memoryFixture) {
	t.Helper()
	if err := os.MkdirAll(fx.autoDir, 0o755); err != nil {
		t.Fatalf("mkdir autoDir: %v", err)
	}
	writeFixture(t, filepath.Join(fx.autoDir, "MEMORY.md"), consolidateIndex)
	writeFixture(t, filepath.Join(fx.autoDir, "order-line-items.md"),
		"---\nname: order-line-items\n---\n\nNeeds [[billing-migration]].\n")
	writeFixture(t, filepath.Join(fx.autoDir, "search-indexer.md"),
		"---\nname: search-indexer\nmetadata:\n  type: project\n---\n\nSlugs are name-derived.\n")
	writeFixture(t, filepath.Join(fx.autoDir, "billing-migration.md"),
		"---\nname: billing-migration\n---\n\nLanded in PR #211.\n")
	writeFixture(t, filepath.Join(fx.autoDir, "inventory-audit.md"),
		"---\nname: inventory-audit\n---\n\nWaiting on the deploy.\n")
}

// treeHash fingerprints every file under dir (relative path + content).
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	var parts []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		parts = append(parts, rel+":"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// postConsolidate calls the endpoint and decodes the typed body.
func postConsolidate(t *testing.T, base, dir, dryRun string, wantStatus int) memoryConsolidateDTO {
	t.Helper()
	qs := url.Values{"path": {dir}}
	if dryRun != "" {
		qs.Set("dry_run", dryRun)
	}
	res, err := http.Post(base+"/api/memory/consolidate?"+qs.Encode(), "application/json", nil)
	if err != nil {
		t.Fatalf("POST consolidate: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != wantStatus {
		body := map[string]any{}
		json.NewDecoder(res.Body).Decode(&body)
		t.Fatalf("status = %d, want %d (body: %v)", res.StatusCode, wantStatus, body)
	}
	var out memoryConsolidateDTO
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return out
}

func TestConsolidateDryRunIsReadOnly(t *testing.T) {
	fx := newMemoryFixture(t, false)
	seedConsolidateFixture(t, fx)
	before := treeHash(t, fx.autoDir)

	out := postConsolidate(t, fx.srv.URL, fx.autoDir, "1", http.StatusOK)
	if !out.DryRun {
		t.Fatal("dryRun = false for dry_run=1")
	}
	if out.Result != nil {
		t.Fatalf("dry run returned a result: %+v", out.Result)
	}
	// 4 entries; search-indexer (DONE) and billing-migration (MERGED…DONE) carry
	// closed markers. order-line-items is open, and inventory-audit is SHIPPED
	// with a "tail =" remainder, which is an open tail.
	if out.Plan.TotalLines != 4 || out.Plan.ClosedCount != 2 {
		t.Fatalf("plan counts = %d lines / %d closed, want 4 / 2", out.Plan.TotalLines, out.Plan.ClosedCount)
	}
	if len(out.Plan.Move) != 1 || out.Plan.Move[0].File != "search-indexer.md" {
		t.Fatalf("Move = %+v, want only search-indexer.md", out.Plan.Move)
	}
	if len(out.Plan.Keep) != 1 || out.Plan.Keep[0].Reason != memconsolidate.ReasonLinked {
		t.Fatalf("Keep = %+v, want billing-migration held by the link guard", out.Plan.Keep)
	}

	if after := treeHash(t, fx.autoDir); after != before {
		t.Fatalf("dry run mutated the auto-memory dir: %s != %s", after, before)
	}
	// It also must not have created a backup — nothing was at risk.
	if entries, err := os.ReadDir(fx.backupsDir); err == nil && len(entries) != 0 {
		t.Fatalf("dry run created %d backup dirs", len(entries))
	}
}

// A missing dry_run parameter must behave like a dry run: the default can never
// be "move the operator's memory files".
func TestConsolidateDefaultsToDryRun(t *testing.T) {
	fx := newMemoryFixture(t, false)
	seedConsolidateFixture(t, fx)
	before := treeHash(t, fx.autoDir)

	out := postConsolidate(t, fx.srv.URL, fx.autoDir, "", http.StatusOK)
	if !out.DryRun || out.Result != nil {
		t.Fatalf("missing dry_run did not default to a dry run: %+v", out)
	}
	if after := treeHash(t, fx.autoDir); after != before {
		t.Fatal("the default path mutated the directory")
	}
}

func TestConsolidateApplyMovesAndBacksUp(t *testing.T) {
	fx := newMemoryFixture(t, false)
	seedConsolidateFixture(t, fx)

	out := postConsolidate(t, fx.srv.URL, fx.autoDir, "0", http.StatusOK)
	if out.DryRun {
		t.Fatal("dryRun = true for dry_run=0")
	}
	if out.Result == nil {
		t.Fatal("apply returned no result")
	}
	if len(out.Result.Moved) != 1 || out.Result.Moved[0] != "search-indexer.md" {
		t.Fatalf("Moved = %v", out.Result.Moved)
	}
	if out.Result.BackupID == "" {
		t.Fatal("apply returned no backup id")
	}

	// The backup holds the ORIGINAL index and the ORIGINAL topic file, mirrored
	// under the timestamp dir by absolute path — one dir for the whole apply.
	entries, err := os.ReadDir(fx.backupsDir)
	if err != nil {
		t.Fatalf("read backups: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != out.Result.BackupID {
		t.Fatalf("backup dirs = %v, want exactly [%s]", entries, out.Result.BackupID)
	}
	tsDir := filepath.Join(fx.backupsDir, out.Result.BackupID)
	// The mirror is keyed on the SYMLINK-RESOLVED path, because that is what the
	// fence hands the apply (on macOS /var/folders is a symlink to /private/var).
	realAutoDir, err := filepath.EvalSymlinks(fx.autoDir)
	if err != nil {
		t.Fatalf("resolve autoDir: %v", err)
	}
	mirrorOf := func(name string) string {
		return filepath.Join(tsDir,
			strings.TrimPrefix(filepath.Join(realAutoDir, name), string(os.PathSeparator)))
	}
	for _, name := range []string{"MEMORY.md", "search-indexer.md"} {
		if _, err := os.Stat(mirrorOf(name)); err != nil {
			t.Fatalf("backup is missing %s: %v", name, err)
		}
	}
	if got := readDisk(t, mirrorOf("MEMORY.md")); got != consolidateIndex {
		t.Fatalf("backed-up index is not the pre-apply content:\n%s", got)
	}

	// On disk: the file moved under closed/, stamped, and left the index.
	if _, err := os.Stat(filepath.Join(fx.autoDir, "search-indexer.md")); !os.IsNotExist(err) {
		t.Fatalf("search-indexer.md is still in the memory dir: %v", err)
	}
	moved := readDisk(t, filepath.Join(fx.autoDir, "closed", "search-indexer.md"))
	if !strings.Contains(moved, "status: closed") || !strings.Contains(moved, "closed_at: ") {
		t.Fatalf("moved file not stamped:\n%s", moved)
	}
	idx := readDisk(t, filepath.Join(fx.autoDir, "MEMORY.md"))
	if strings.Contains(idx, "search-indexer.md") {
		t.Fatalf("index still carries the consolidated line:\n%s", idx)
	}
	for _, keep := range []string{"order-line-items.md", "billing-migration.md", "inventory-audit.md"} {
		if !strings.Contains(idx, keep) {
			t.Fatalf("index lost %s:\n%s", keep, idx)
		}
	}
	ledger := readDisk(t, filepath.Join(fx.autoDir, "closed", "INDEX.md"))
	if !strings.Contains(ledger, "[Search indexer](search-indexer.md)") {
		t.Fatalf("ledger missing the consolidated line:\n%s", ledger)
	}

	// Idempotence: a second apply has nothing left to do and takes no backup.
	again := postConsolidate(t, fx.srv.URL, fx.autoDir, "0", http.StatusOK)
	if len(again.Result.Moved) != 0 {
		t.Fatalf("second apply moved %v", again.Result.Moved)
	}
	if entries, err := os.ReadDir(fx.backupsDir); err == nil && len(entries) != 1 {
		t.Fatalf("second apply created another backup dir (now %d)", len(entries))
	}
}

func TestConsolidateReadOnlyModeRefusesApply(t *testing.T) {
	t.Setenv(sysedit.EnvReadOnly, "1")
	fx := newMemoryFixture(t, false)
	seedConsolidateFixture(t, fx)
	before := treeHash(t, fx.autoDir)

	postConsolidate(t, fx.srv.URL, fx.autoDir, "0", http.StatusForbidden)
	if after := treeHash(t, fx.autoDir); after != before {
		t.Fatal("a refused apply still touched the directory")
	}
	// A dry run stays available in readonly mode — it writes nothing.
	out := postConsolidate(t, fx.srv.URL, fx.autoDir, "1", http.StatusOK)
	if len(out.Plan.Move) != 1 {
		t.Fatalf("dry run in readonly mode = %+v", out.Plan)
	}
}

func TestConsolidateFencesThePath(t *testing.T) {
	fx := newMemoryFixture(t, true) // seeds all three roots
	seedConsolidateFixture(t, fx)

	cases := []struct {
		name string
		path string
		want int
	}{
		{"empty", "", http.StatusBadRequest},
		{"relative", "memory", http.StatusBadRequest},
		{"parent walk", filepath.Join(fx.autoDir, ".."), http.StatusBadRequest},
		{"arbitrary dir", t.TempDir(), http.StatusBadRequest},
		{"the project root itself", fx.projectPath, http.StatusBadRequest},
		{"the serena root", filepath.Join(fx.projectPath, ".serena", "memories"), http.StatusBadRequest},
		{"a file, not the dir", filepath.Join(fx.autoDir, "MEMORY.md"), http.StatusBadRequest},
		{"the closed subdir", filepath.Join(fx.autoDir, "closed"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			postConsolidate(t, fx.srv.URL, tc.path, "0", tc.want)
		})
	}
	// …and the real root still resolves.
	postConsolidate(t, fx.srv.URL, fx.autoDir, "1", http.StatusOK)
}

func TestConsolidateMissingIndexIs404(t *testing.T) {
	fx := newMemoryFixture(t, false)
	if err := os.MkdirAll(fx.autoDir, 0o755); err != nil {
		t.Fatalf("mkdir autoDir: %v", err)
	}
	postConsolidate(t, fx.srv.URL, fx.autoDir, "1", http.StatusNotFound)
}
