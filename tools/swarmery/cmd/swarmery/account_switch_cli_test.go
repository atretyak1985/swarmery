package main

// Shape tests for `account switch` / `account move-session`. The rules live
// in internal/acctops (covered there); what is pinned here is argument
// handling, and that `account env` still prints zero or one line now that the
// new subcommands exist.

import (
	"bytes"
	"strings"
	"testing"
)

func TestAccountSwitchNeedsAKey(t *testing.T) {
	fakeHome(t, "default")
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"--force"}, {"a", "b"}, {"--bogus"}} {
		err := accountSwitch(args, &out)
		if err == nil || !strings.Contains(err.Error(), "usage: swarmery account switch") {
			t.Errorf("switch %v: err = %v, want usage", args, err)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a usage error printed to stdout: %q", out.String())
	}
}

func TestAccountMoveSessionNeedsTo(t *testing.T) {
	fakeHome(t, "default")
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"some-uuid"}, {"--to", "default"}, {"u", "--bogus"}} {
		err := accountMoveSession(args, &out)
		if err == nil || !strings.Contains(err.Error(), "usage: swarmery account move-session") {
			t.Errorf("move-session %v: err = %v, want usage", args, err)
		}
	}
}

// An estate-less --estate is refused with both remedies and prints nothing on
// stdout.
func TestAccountSwitchRefusesEstateless(t *testing.T) {
	fakeHome(t, "default", "work")
	dir := project(t, "work")
	var out bytes.Buffer
	err := cmdAccountTo([]string{"switch", "default", "--estate", dir}, &out)
	if err == nil || !strings.Contains(err.Error(), "swarmery account use") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

// cmdAccountTo runs the new subcommands against out (the dispatcher itself
// writes to os.Stdout).
func cmdAccountTo(args []string, out *bytes.Buffer) error {
	switch args[0] {
	case "switch":
		return accountSwitch(args[1:], out)
	case "move-session":
		return accountMoveSession(args[1:], out)
	}
	return nil
}

func TestAccountEnvStillZeroOrOneLine(t *testing.T) {
	fakeHome(t, "default", "work")
	for _, account := range []string{"", "default", "work"} {
		dir := project(t, account)
		var out bytes.Buffer
		if err := accountEnv([]string{"--path", dir}, &out); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(out.String(), "\n"); n > 1 {
			t.Errorf("account env (%q) printed %d lines", account, n)
		}
	}
}
