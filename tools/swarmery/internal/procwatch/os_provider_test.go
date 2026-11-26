package procwatch

import "testing"

// The STAT/tty table from the phase doc: T+no-tty is detached, T+tty is a
// Ctrl+Z in an ordinary terminal (still fg-able, not detached), and a
// not-stopped process is never detached regardless of tty.
func TestIsDetachedStat(t *testing.T) {
	cases := []struct {
		name string
		stat string
		tty  string
		want bool
	}{
		{"stopped, no tty (?? marker)", "T", "??", true},
		{"stopped, no tty (empty)", "T", "", true},
		{"stopped, with tty", "T", "ttys005", false},
		{"stopped+ (foreground group leader), no tty", "T+", "??", true},
		{"running, no tty", "S", "??", false},
		{"running, with tty", "S", "ttys005", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isDetachedStat(c.stat, c.tty); got != c.want {
				t.Errorf("isDetachedStat(%q, %q) = %v, want %v", c.stat, c.tty, got, c.want)
			}
		})
	}
}
