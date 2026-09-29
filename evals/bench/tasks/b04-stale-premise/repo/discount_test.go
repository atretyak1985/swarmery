package orders

import "testing"

func TestSubtotal(t *testing.T) {
	lines := []Line{{"A-100", 2, 250}, {"B-200", 1, 999}}
	if got := Subtotal(lines); got != 1499 {
		t.Errorf("Subtotal = %d, want 1499", got)
	}
}

func TestApplyDiscount(t *testing.T) {
	cases := []struct{ subtotal, discount, want int }{
		{1000, 250, 750},
		{1000, 0, 1000},
		{1000, 1000, 0},
		{500, 800, 0},
	}
	for _, c := range cases {
		if got := ApplyDiscount(c.subtotal, c.discount); got != c.want {
			t.Errorf("ApplyDiscount(%d, %d) = %d, want %d", c.subtotal, c.discount, got, c.want)
		}
	}
}
