package orders

import "testing"

func TestTotal(t *testing.T) {
	cases := []struct {
		name  string
		lines []Line
		want  int
	}{
		{"empty", nil, 0},
		{"one line", []Line{{"A-100", 2, 250}}, 500},
		{"three lines", []Line{{"A-100", 2, 250}, {"B-200", 1, 999}, {"C-300", 3, 100}}, 1799},
	}
	for _, c := range cases {
		if got := Total(c.lines); got != c.want {
			t.Errorf("%s: Total = %d, want %d", c.name, got, c.want)
		}
	}
}
