package worktree

import (
	"testing"
	"time"
)

// TestDefaultTimeout: only the whole-tree commands get the long bound.
func TestDefaultTimeout(t *testing.T) {
	cases := []struct {
		args []string
		want time.Duration
	}{
		{[]string{"worktree", "add", "-b", "swarm/x", "/p", "abc"}, checkoutTimeout},
		{[]string{"worktree", "remove", "--force", "/p"}, checkoutTimeout},
		{[]string{"worktree", "list", "--porcelain"}, gitTimeout},
		{[]string{"worktree", "prune"}, gitTimeout},
		{[]string{"rev-parse", "HEAD"}, gitTimeout},
		{[]string{"worktree"}, gitTimeout},
		{nil, gitTimeout},
	}
	for _, tc := range cases {
		if got := defaultTimeout(tc.args); got != tc.want {
			t.Errorf("defaultTimeout(%q) = %s, want %s", tc.args, got, tc.want)
		}
	}
	if checkoutTimeout <= gitTimeout {
		t.Errorf("checkoutTimeout %s must exceed gitTimeout %s", checkoutTimeout, gitTimeout)
	}
}
