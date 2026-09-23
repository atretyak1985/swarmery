package acctops

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const (
	uuid1     = "4afdca62-0000-4000-8000-000000000001"
	weirdName = "-p-src-php--claude-worktrees-feature-X" // NOT an encoding of cwd /p/src/php
	decoyName = "-p-src-php"                             // what an encoder would compute
)

// moveFixture: a fake HOME with default and insart, a transcript for uuid1
// under default's projects/<weirdName>/ (plus its sibling dir and a memory
// dir), a populated decoy dir, and a database row recording cwd /p/src/php.
func moveFixture(t *testing.T, status, procState string) (home, dbPath string) {
	t.Helper()
	home, _ = fakeHome(t)
	src := filepath.Join(home, ".claude", "projects", weirdName)
	mustWrite(t, filepath.Join(src, uuid1+".jsonl"), `{"type":"user"}`+"\n")
	mustWrite(t, filepath.Join(src, uuid1, "subagents", "agent-1.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(src, uuid1, "tool-results", "r.txt"), "result\n")
	mustWrite(t, filepath.Join(src, "memory", "MEMORY.md"), "- [a](a.md)\n")
	mustWrite(t, filepath.Join(src, "memory", "a.md"), "a\n")
	mustWrite(t, filepath.Join(home, ".claude", "projects", decoyName, "other-session.jsonl"), "{}\n")

	dbPath = filepath.Join(t.TempDir(), "s.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p/src/php', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at, cwd, status, proc_state, account)
		VALUES (1, ?, 'x', '/p/src/php', ?, ?, 'default')`, uuid1, status, procState); err != nil {
		t.Fatal(err)
	}
	return home, dbPath
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dbAccount(t *testing.T, dbPath string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var a string
	if err := db.QueryRow(`SELECT account FROM sessions WHERE session_uuid = ?`, uuid1).Scan(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func setCwd(t *testing.T, dbPath, cwd string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET cwd = ? WHERE session_uuid = ?`, cwd, uuid1); err != nil {
		t.Fatal(err)
	}
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err == nil && p != root {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// The destination name is the LOCATED one, byte-for-byte; a changed recorded
// cwd, or a wrong --cwd, changes no destination path.
func TestMoveSessionPreservesDirectoryName(t *testing.T) {
	home, dbPath := moveFixture(t, "completed", "")
	dstProjects := filepath.Join(home, ".claude-insart", "projects")

	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Name != weirdName || len(rep.Items) != 3 {
		t.Fatalf("name %q, items %+v", rep.Name, rep.Items)
	}
	for _, it := range rep.Items {
		if !strings.HasPrefix(it.Dst, dstProjects+string(os.PathSeparator)+weirdName+string(os.PathSeparator)) {
			t.Errorf("destination %s does not carry %s verbatim", it.Dst, weirdName)
		}
		if strings.Contains(it.Dst, string(os.PathSeparator)+decoyName+string(os.PathSeparator)) {
			t.Errorf("destination %s uses the computed name", it.Dst)
		}
	}
	if len(listTree(t, dstProjects)) != 0 {
		t.Error("a dry run created something")
	}

	setCwd(t, dbPath, "/somewhere/else/entirely")
	rep2, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	rep3, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath, Cwd: "/nonexistent/not/a/project"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Items, rep2.Items) || !reflect.DeepEqual(rep.Items, rep3.Items) {
		t.Error("a different cwd changed a destination path")
	}
	if rep3.Cwd != "/nonexistent/not/a/project" || !strings.Contains(rep3.ResumeCommand(), "--path /nonexistent/not/a/project") {
		t.Errorf("--cwd did not reach the resume command: %s", rep3.ResumeCommand())
	}

	// The real move lands under the located name, and nowhere else.
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{uuid1 + ".jsonl", uuid1 + "/subagents/agent-1.jsonl", "memory/MEMORY.md"} {
		if !exists_(filepath.Join(dstProjects, weirdName, rel)) {
			t.Errorf("missing %s under the located name", rel)
		}
	}
	if exists_(filepath.Join(dstProjects, decoyName)) {
		t.Error("the computed (decoy) directory was created")
	}
}

func TestMoveSessionRefusesWhenTranscriptNotFound(t *testing.T) {
	home, _ := fakeHome(t)
	src := filepath.Join(home, ".claude")
	dstProjects := filepath.Join(home, ".claude-insart", "projects")
	_, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", From: "default", DBPath: noDB(t)})
	if err == nil {
		t.Fatal("moved a session that is not there")
	}
	glob := filepath.Join(src, "projects", "*", uuid1+".jsonl")
	if !strings.Contains(err.Error(), src) || !strings.Contains(err.Error(), glob) {
		t.Errorf("error does not name the config dir and the glob: %v", err)
	}
	if n := len(listTree(t, dstProjects)); n != 0 {
		t.Errorf("created %d entries under the destination", n)
	}
}

func TestMoveSessionCopiesAndRepointsTheRow(t *testing.T) {
	home, dbPath := moveFixture(t, "completed", "")
	// A target file with IDENTICAL content is left alone and counted as kept.
	dstMem := filepath.Join(home, ".claude-insart", "projects", weirdName, "memory", "a.md")
	mustWrite(t, dstMem, "a\n")
	fi0, err := os.Stat(dstMem)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.RowUpdated || dbAccount(t, dbPath) != "insart" {
		t.Errorf("row not re-pointed (report %+v)", rep)
	}
	if fi1, err := os.Stat(dstMem); err != nil || !os.SameFile(fi0, fi1) {
		t.Error("an identical target file was rewritten")
	}
	if rep.Copied != 4 || rep.Kept != 1 || len(rep.Conflicts) != 0 {
		t.Errorf("copied %d kept %d conflicts %v, want 4/1/none", rep.Copied, rep.Kept, rep.Conflicts)
	}
	src := filepath.Join(home, ".claude", "projects", weirdName, uuid1+".jsonl")
	if !exists_(src) {
		t.Error("the source transcript is gone — a move, not a copy")
	}
	lines := joined(rep.Lines())
	for _, want := range []string{"account:     default -> insart", "located:     " + weirdName,
		"database:    sessions row now under insart", "--resume " + uuid1, "never --fork-session"} {
		if !strings.Contains(lines, want) {
			t.Errorf("output lacks %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "/private/tmp") {
		t.Error("output names a scratchpad path")
	}

	// Idempotent: the row now says insart, so re-running the same move is a
	// no-op that succeeds with exactly one line.
	again, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
	if err != nil || !again.NoOp {
		t.Fatalf("second move: noop=%v err=%v", again.NoOp, err)
	}
	if l := again.Lines(); len(l) != 1 || !strings.Contains(l[0], "nothing to do") {
		t.Errorf("no-op output = %q, want one 'nothing to do' line", l)
	}
	// Naming the source explicitly: every file is identical, still a no-op.
	again, err = MoveSession(MoveOptions{UUID: uuid1, To: "insart", From: "default", DBPath: dbPath})
	if err != nil || !again.NoOp || len(again.Lines()) != 1 || !strings.Contains(again.Lines()[0], "identical") {
		t.Errorf("re-run with --from: noop=%v err=%v lines=%q", again.NoOp, err, again.Lines())
	}
	if dbAccount(t, dbPath) != "insart" {
		t.Error("a no-op changed the row")
	}
}

// A->B, the transcript changes in B, B->A: the move back is refused before
// anything is written (listing the differing path); with --overwrite A ends up
// with B's content and A's old file is preserved as .pre-move-<stamp>.
func TestMoveSessionRoundTripRefusesThenPreserves(t *testing.T) {
	home, dbPath := moveFixture(t, "completed", "")
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath}); err != nil {
		t.Fatal(err)
	}
	aFile := filepath.Join(home, ".claude", "projects", weirdName, uuid1+".jsonl")
	bFile := filepath.Join(home, ".claude-insart", "projects", weirdName, uuid1+".jsonl")
	aOld, _ := os.ReadFile(aFile)
	bNew := string(aOld) + `{"type":"assistant"}` + "\n"
	mustWrite(t, bFile, bNew)
	aDir := filepath.Dir(aFile)
	before := listTree(t, aDir)

	_, err := MoveSession(MoveOptions{UUID: uuid1, To: "default", DBPath: dbPath})
	if !errors.Is(err, ErrDestinationDiffers) || !strings.Contains(err.Error(), aFile) ||
		!strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("move back without --overwrite: %v", err)
	}
	if b, _ := os.ReadFile(aFile); string(b) != string(aOld) {
		t.Error("a refused move changed the destination")
	}
	if !reflect.DeepEqual(listTree(t, aDir), before) || dbAccount(t, dbPath) != "insart" {
		t.Error("a refused move wrote something")
	}

	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "default", DBPath: dbPath, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(aFile); string(b) != bNew {
		t.Error("A does not carry B's content after --overwrite")
	}
	backups, _ := filepath.Glob(aFile + ".pre-move-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one .pre-move-* file", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != string(aOld) {
		t.Error("the preserved file does not hold A's old content")
	}
	if rep.Copied != 1 || rep.Kept != 4 || len(rep.Preserved) != 1 || dbAccount(t, dbPath) != "default" {
		t.Errorf("overwrite: copied %d kept %d preserved %v row %s", rep.Copied, rep.Kept, rep.Preserved, dbAccount(t, dbPath))
	}
	if !strings.Contains(joined(rep.Lines()), "preserved:   "+aFile+" -> "+backups[0]) {
		t.Errorf("output does not name the preserved file:\n%s", joined(rep.Lines()))
	}
}

func TestMoveSessionRefusesALiveSession(t *testing.T) {
	for _, tc := range []struct{ status, proc string }{{"active", ""}, {"waiting_approval", ""}, {"idle", "running"}} {
		home, dbPath := moveFixture(t, tc.status, tc.proc)
		dstProjects := filepath.Join(home, ".claude-insart", "projects")
		_, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
		if !errors.Is(err, ErrSessionLive) {
			t.Fatalf("%+v: err = %v", tc, err)
		}
		if strings.Contains(err.Error(), "fork-session") {
			t.Error("a refusal proposes a fork")
		}
		if !strings.Contains(err.Error(), "pgrep") {
			t.Error("the refusal does not print the process-finding command")
		}
		if n := len(listTree(t, dstProjects)); n != 0 {
			t.Errorf("a refused move created %d entries", n)
		}
		if dbAccount(t, dbPath) != "default" {
			t.Error("a refused move changed the row")
		}
		if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath, Force: true}); err != nil {
			t.Errorf("--force: %v", err)
		}
	}
}

func TestMoveSessionAmbiguousAndSelector(t *testing.T) {
	home, dbPath := moveFixture(t, "completed", "")
	second := filepath.Join(home, ".claude", "projects", "-other-dir")
	mustWrite(t, filepath.Join(second, uuid1+".jsonl"), "{}\n")
	_, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath})
	if err == nil || !strings.Contains(err.Error(), "--project-dir") ||
		!strings.Contains(err.Error(), weirdName) || !strings.Contains(err.Error(), "-other-dir") {
		t.Fatalf("ambiguous: %v", err)
	}
	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath, ProjectDir: "-other-dir"})
	if err != nil || rep.Name != "-other-dir" || len(rep.Items) != 1 {
		t.Fatalf("selector: %+v %v", rep, err)
	}
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DryRun: true, DBPath: dbPath, ProjectDir: "-nope"}); err == nil {
		t.Error("a selector naming no located hit was accepted")
	}
}

func TestMoveSessionWithoutADatabase(t *testing.T) {
	home, _ := moveFixture(t, "completed", "")
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: noDB(t)}); err == nil ||
		!strings.Contains(err.Error(), "--from") {
		t.Errorf("no db, no --from: %v", err)
	}
	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", From: "default", DBPath: noDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RowUpdated || !strings.Contains(joined(rep.Lines()), "database:    not updated") {
		t.Errorf("report = %+v", rep)
	}
	// With no row, liveness is unchecked: a warning (not a refusal) prints the
	// process-finding command.
	if out := joined(rep.Lines()); !strings.Contains(out, "warning:     no sessions row") ||
		!strings.Contains(out, FindProcessesCommand) {
		t.Errorf("no liveness warning:\n%s", out)
	}
	if !exists_(filepath.Join(home, ".claude-insart", "projects", weirdName, uuid1+".jsonl")) {
		t.Error("transcript not copied")
	}
	if !strings.Contains(rep.ResumeCommand(), "'<the session") {
		t.Errorf("unknown cwd placeholder: %s", rep.ResumeCommand())
	}
}

func TestMoveSessionInputValidation(t *testing.T) {
	_, dbPath := moveFixture(t, "completed", "")
	cases := []MoveOptions{
		{UUID: "../x", To: "insart", DBPath: dbPath},
		{UUID: "*", To: "insart", DBPath: dbPath},
		{UUID: uuid1, To: "../x", DBPath: dbPath},
		{UUID: uuid1, To: "nosuch", DBPath: dbPath},
		{UUID: uuid1, To: "insart", From: "insart", DBPath: dbPath}, // disagrees with the row
		{UUID: uuid1, To: "insart", From: "nosuch", DBPath: dbPath}, // disagrees with the row
	}
	for _, c := range cases {
		if _, err := MoveSession(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	// Already on the target, transcript there: a no-op, not an error.
	if rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "default", DBPath: dbPath}); err != nil || !rep.NoOp {
		t.Errorf("already there: noop=%v err=%v", rep.NoOp, err)
	}
	// Already on the target by the row, but the target holds no transcript.
	_, dbPath2 := moveFixture(t, "completed", "")
	db, err := sql.Open("sqlite", "file:"+dbPath2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET account = 'insart'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath2}); err == nil ||
		!strings.Contains(err.Error(), "--from") {
		t.Errorf("row on target, no files there: %v", err)
	}
	// No row and --from equal to --to.
	if _, err := MoveSession(MoveOptions{UUID: uuid1, To: "default", From: "default", DBPath: noDB(t)}); err == nil ||
		!strings.Contains(err.Error(), "already under") {
		t.Errorf("--from == --to: %v", err)
	}
}

func TestMoveReportResumeNote(t *testing.T) {
	home, _ := fakeHome(t)
	proj := filepath.Join(home, "work", "proj dir")
	writeBinding(t, proj, `{"swarmery":{"claudeAccount":"insart"}}`)
	r := MoveReport{UUID: uuid1, To: "insart", Cwd: proj}
	if r.ResumeAccountNote() != "" {
		t.Error("a path already on the target got a note")
	}
	if !strings.Contains(r.ResumeCommand(), "'"+proj+"'") {
		t.Errorf("path not quoted: %s", r.ResumeCommand())
	}
	r.To = "default"
	if note := r.ResumeAccountNote(); !strings.Contains(note, "swarmery account use default --path") {
		t.Errorf("note = %q", note)
	}
	if (MoveReport{}).ResumeAccountNote() != "" {
		t.Error("unknown cwd got a note")
	}
}

// Symlinks are never recreated: one inside <uuid>/ or memory/ is refused and
// listed, a symlinked memory/ is skipped whole and listed, the rest copies.
func TestMoveSessionRefusesSymlinks(t *testing.T) {
	home, dbPath := moveFixture(t, "completed", "")
	src := filepath.Join(home, ".claude", "projects", weirdName)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, outside, "outside\n")
	if err := os.Symlink(outside, filepath.Join(src, uuid1, "tool-results", "link.txt")); err != nil {
		t.Fatal(err)
	}
	// Replace memory/ with a symlink to a real directory.
	realMem := filepath.Join(t.TempDir(), "mem")
	mustWrite(t, filepath.Join(realMem, "x.md"), "x\n")
	if err := os.RemoveAll(filepath.Join(src, "memory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realMem, filepath.Join(src, "memory")); err != nil {
		t.Fatal(err)
	}

	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	dstDir := filepath.Join(home, ".claude-insart", "projects", weirdName)
	if exists_(filepath.Join(dstDir, uuid1, "tool-results", "link.txt")) {
		t.Error("a symlink inside the session dir was recreated")
	}
	if exists_(filepath.Join(dstDir, "memory")) {
		t.Error("a symlinked memory dir was followed")
	}
	if !exists_(filepath.Join(dstDir, uuid1, "tool-results", "r.txt")) || rep.Copied != 3 {
		t.Errorf("regular files not copied: copied %d", rep.Copied)
	}
	out := joined(rep.Lines())
	if !strings.Contains(out, "refused:     symlink not copied: "+filepath.Join(src, uuid1, "tool-results", "link.txt")) ||
		!strings.Contains(out, "skipped:     memory "+filepath.Join(src, "memory")+" is a symlink") {
		t.Errorf("symlinks not reported:\n%s", out)
	}
}

// A destination directory that is a symlink refuses the move before anything
// is written.
func TestMoveSessionRefusesSymlinkedDestinationDir(t *testing.T) {
	for _, rel := range []string{"", "memory"} {
		home, dbPath := moveFixture(t, "completed", "")
		dstProjects := filepath.Join(home, ".claude-insart", "projects")
		elsewhere := t.TempDir()
		link := filepath.Join(dstProjects, weirdName)
		if rel != "" {
			if err := os.MkdirAll(link, 0o755); err != nil {
				t.Fatal(err)
			}
			link = filepath.Join(link, rel)
		}
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		_, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
		if err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Errorf("%q: err = %v, want a symlink refusal", rel, err)
		}
		if n := len(listTree(t, elsewhere)); n != 0 {
			t.Errorf("%q: wrote %d entries through the symlink", rel, n)
		}
		if dbAccount(t, dbPath) != "default" {
			t.Errorf("%q: the row moved", rel)
		}
	}
}

// copyFile never leaves a partial file under the final name, and never
// replaces a destination that appeared after the scan.
func TestCopyFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, src, "payload")
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(out, "f")
	if err := copyFile(src, dst, fi); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Errorf("content = %q", b)
	}
	if fi2, _ := os.Stat(dst); fi2.Mode().Perm() != fi.Mode().Perm() || !fi2.ModTime().Equal(fi.ModTime()) {
		t.Errorf("mode/mtime not preserved: %v %v", fi2.Mode(), fi2.ModTime())
	}

	// Reading a directory fails mid-copy: no file under the final name, no temp.
	bad := filepath.Join(out, "g")
	if err := copyFile(dir, bad, fi); err == nil {
		t.Error("copying a directory succeeded")
	}
	// A destination that already exists is never replaced.
	mustWrite(t, filepath.Join(out, "h"), "theirs")
	if err := copyFile(src, filepath.Join(out, "h"), fi); err == nil {
		t.Error("an existing destination was replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(out, "h")); string(b) != "theirs" {
		t.Error("the existing destination changed")
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"f", "h"}) {
		t.Errorf("destination dir = %v, want only f and h (no partial, no temp)", names)
	}
	// A missing source fails before any temp is created.
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(out, "m"), fi); err == nil {
		t.Error("a missing source copied")
	}
}

// A copy that fails midway reports what landed before the failure, leaves no
// partial file, and does not touch the row.
func TestMoveSessionCopyFailureReportsProgress(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home, dbPath := moveFixture(t, "completed", "")
	locked := filepath.Join(home, ".claude-insart", "projects", weirdName, uuid1, "tool-results")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	rep, err := MoveSession(MoveOptions{UUID: uuid1, To: "insart", DBPath: dbPath})
	if !errors.Is(err, ErrCopyFailed) || !rep.Failed {
		t.Fatalf("err = %v failed=%v, want ErrCopyFailed", err, rep.Failed)
	}
	if rep.Copied != 2 || !strings.Contains(err.Error(), "after 2 file(s)") {
		t.Errorf("copied %d, err %v — want the 2 files that landed first", rep.Copied, err)
	}
	out := joined(rep.Lines())
	if !strings.Contains(out, "files:       2 copied before the failure") ||
		!strings.Contains(out, "database:    not updated — the copy failed") {
		t.Errorf("failure report:\n%s", out)
	}
	if entries, _ := os.ReadDir(locked); len(entries) != 0 {
		t.Errorf("the locked dir holds %d entries, want none (no partial, no temp)", len(entries))
	}
	if dbAccount(t, dbPath) != "default" {
		t.Error("a failed copy re-pointed the row")
	}
}

// preserve never deletes: a taken .pre-move name gets a -N suffix.
func TestPreserveNeverClobbers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	mustWrite(t, p+".pre-move-S", "older")
	mustWrite(t, p, "current")
	got, err := preserve(p, "S")
	if err != nil || got != p+".pre-move-S-1" {
		t.Fatalf("preserve = %q %v", got, err)
	}
	if b, _ := os.ReadFile(p + ".pre-move-S"); string(b) != "older" {
		t.Error("an older backup was clobbered")
	}
	if _, err := preserve(filepath.Join(dir, "missing"), "S"); err == nil {
		t.Error("preserving a missing file succeeded")
	}
}

// A name too long to carry the .pre-move suffix still gets a backup, under a
// deterministically shortened name that fits one path element.
func TestPreserveLongName(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("é", 10) + strings.Repeat("a", 225) // 245 bytes, multibyte head
	p := filepath.Join(dir, name)
	mustWrite(t, p, "current")
	stamp := time.Date(2026, 9, 23, 1, 2, 3, 0, time.UTC).Format(preMoveStamp)
	got, err := preserve(p, stamp)
	if err != nil {
		t.Fatalf("preserve: %v", err)
	}
	base := filepath.Base(got)
	if len(base) > maxNameBytes || !utf8.ValidString(base) || !strings.HasSuffix(base, ".pre-move-"+stamp) {
		t.Errorf("backup name %q (%d bytes)", base, len(base))
	}
	if want := backupBaseName(name, 255-len(".pre-move-"+stamp)-3); !strings.HasPrefix(base, want) || want == name {
		t.Errorf("backup %q does not use the shortened base %q", base, want)
	}
	if b, _ := os.ReadFile(got); string(b) != "current" {
		t.Error("the backup does not hold the preserved content")
	}
	// Taken again: same shortened base, -1 suffix, still within the limit.
	mustWrite(t, p, "next")
	got2, err := preserve(p, stamp)
	if err != nil || got2 != got+"-1" || len(filepath.Base(got2)) > maxNameBytes {
		t.Errorf("second preserve = %q %v", got2, err)
	}
}

// A probe error other than not-exist stops the search (it used to spin), and
// the -N search is capped.
func TestPreserveStopsOnProbeErrorAndCap(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	mustWrite(t, file, "x")
	// <file>/child: every probe under a regular file fails with ENOTDIR.
	if _, err := preserve(filepath.Join(file, "child"), "S"); err == nil || !strings.Contains(err.Error(), "probe backup name") {
		t.Errorf("preserve under a file = %v, want the probe error", err)
	}

	p := filepath.Join(dir, "f")
	mustWrite(t, p, "current")
	mustWrite(t, p+".pre-move-S", "taken")
	for n := 1; n < maxPreserveAttempts; n++ {
		mustWrite(t, fmt.Sprintf("%s.pre-move-S-%d", p, n), "taken")
	}
	if _, err := preserve(p, "S"); err == nil || !strings.Contains(err.Error(), "after 100 attempts") {
		t.Errorf("preserve with every name taken = %v, want the cap error", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "current" {
		t.Error("the file was moved despite the cap")
	}
}

// A file created at the final path inside the write window (after the scan,
// after the temp is written, just before it is put in place) is refused and
// left untouched — on the hard-link path and on the no-link fallback.
func TestCopyFileRefusesDestinationAppearingMidWrite(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	mustWrite(t, src, "ours")
	fi, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		link func(oldname, newname string) error
	}{
		{"link", os.Link},
		{"fallback", func(string, string) error { return &os.LinkError{Op: "link", Err: errors.ErrUnsupported} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			dst := filepath.Join(out, "f")
			orig := linkIntoPlace
			t.Cleanup(func() { linkIntoPlace = orig })
			linkIntoPlace = func(oldname, newname string) error {
				mustWrite(t, newname, "theirs")
				return tc.link(oldname, newname)
			}
			if err := copyFile(src, dst, fi); err == nil || !strings.Contains(err.Error(), "appeared during the move") {
				t.Errorf("copyFile = %v, want a refusal", err)
			}
			if b, _ := os.ReadFile(dst); string(b) != "theirs" {
				t.Errorf("destination = %q, want it untouched", b)
			}
			if entries, _ := os.ReadDir(out); len(entries) != 1 {
				t.Errorf("destination dir holds %d entries, want only f (no temp left)", len(entries))
			}
		})
	}

	// The fallback still copies when nothing is in the way.
	orig := linkIntoPlace
	t.Cleanup(func() { linkIntoPlace = orig })
	linkIntoPlace = func(string, string) error { return &os.LinkError{Op: "link", Err: errors.ErrUnsupported} }
	out := t.TempDir()
	if err := copyFile(src, filepath.Join(out, "g"), fi); err != nil {
		t.Fatalf("fallback copy: %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 1 || entries[0].Name() != "g" {
		t.Errorf("fallback left %v, want only g", entries)
	}
}

// Printed names and cwds cannot carry terminal control sequences.
func TestMoveReportLinesAreSanitized(t *testing.T) {
	esc := "\x1b]0;pwned\x07\x1b[2J"
	r := MoveReport{UUID: uuid1, From: "default", To: "insart", Name: "-dir" + esc,
		Items: []CopyItem{{Kind: "transcript", Src: "/a/-dir" + esc + "/x.jsonl", Dst: "/b/-dir" + esc + "/x.jsonl"}},
		Cwd:   "/work/" + esc, RowUpdated: true}
	for _, l := range r.Lines() {
		if strings.ContainsFunc(l, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			t.Errorf("control character in %q", l)
		}
	}
	if got := safe("a\x1bb\u009bc\nd"); got != "a?b?c?d" {
		t.Errorf("safe = %q", got)
	}
	if got := safeJoin([]string{"x\x1b", "y"}); got != "x?\n  y" {
		t.Errorf("safeJoin = %q", got)
	}
	noop := MoveReport{NoOp: true, Note: "nothing to do " + esc}
	if l := noop.Lines(); len(l) != 1 || strings.ContainsRune(l[0], 0x1b) {
		t.Errorf("no-op lines = %q", l)
	}
}

// Every classification: absent, identical, different size, same size but
// different bytes, and a non-regular destination.
func TestCompareDest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, src, "abc")
	fi, _ := os.Stat(src)
	same, size, diffBytes := filepath.Join(dir, "same"), filepath.Join(dir, "size"), filepath.Join(dir, "bytes")
	mustWrite(t, same, "abc")
	mustWrite(t, size, "abcd")
	mustWrite(t, diffBytes, "abd")
	for p, want := range map[string]fileState{
		filepath.Join(dir, "none"): stateNew, same: stateSame, size: stateDiffers, diffBytes: stateDiffers, dir: stateDiffers,
	} {
		if got, err := compareDest(src, p, fi); err != nil || got != want {
			t.Errorf("compareDest(%s) = %v %v, want %v", filepath.Base(p), got, err, want)
		}
	}
}
