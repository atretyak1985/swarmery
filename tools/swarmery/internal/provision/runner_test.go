package provision

import "testing"

func TestTail(t *testing.T) {
	if got := tail("  hello  ", 100); got != "hello" {
		t.Fatalf("trim: %q", got)
	}
	if got := tail("abcdef", 3); got != "def" {
		t.Fatalf("cap: %q", got)
	}
}
