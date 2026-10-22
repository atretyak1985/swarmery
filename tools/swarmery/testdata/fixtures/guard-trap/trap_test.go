package trap

import "testing"

func TestSumTo(t *testing.T) {
	cases := []struct{ n, want int }{
		{n: -3, want: 0},
		{n: 0, want: 0},
		{n: 1, want: 1},
		{n: 3, want: 6},
		{n: 10, want: 55},
	}
	for _, c := range cases {
		if got := SumTo(c.n); got != c.want {
			t.Errorf("SumTo(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}
