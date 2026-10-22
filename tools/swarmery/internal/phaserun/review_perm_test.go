package phaserun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// A reviewer reads; it never runs a command. Its default argv is therefore the
// read-only site's: dontAsk with Read/Grep/Glob allowed, never bypassPermissions.
// The deny list (edit tools, Bash, the daemon API) still rides along unchanged.
func TestReviewSpawnArgsAreLeastPrivilege(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
	for _, k := range []string{"SWARMERY_VERIFY_PERMISSION_MODE", "SWARMERY_REVIEW_PERMISSION_MODE", "SWARMERY_PERMISSION_MODE"} {
		t.Setenv(k, "")
	}
	bin := t.TempDir()
	argsOut := filepath.Join(t.TempDir(), "args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$REVIEW_ARGS_OUT\"\necho '- no blocking findings'\necho 'VERDICT: PASS'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REVIEW_ARGS_OUT", argsOut)

	s, _, p1, info := reviewFixture(t)
	s.Review = verify.ClaudeRunner{}
	dir, base := gitRepo(t)
	if verdict, _ := s.reviewRun(p1, info, worktree.Acquired{Path: dir, Branch: "swarm/x", StartPoint: base}, "done"); verdict != "pass" {
		t.Fatalf("reviewRun verdict = %q, want pass", verdict)
	}
	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("the stand-in claude never ran: %v", err)
	}
	argv := string(raw)
	if strings.Contains(argv, "bypassPermissions") {
		t.Errorf("reviewer argv carries bypassPermissions:\n%s", argv)
	}
	for _, want := range []string{"--permission-mode\ndontAsk\n", "--allowedTools\nRead,Grep,Glob\n"} {
		if !strings.Contains(argv, want) {
			t.Errorf("reviewer argv lacks %q:\n%s", want, argv)
		}
	}
}
