package orders

import (
	"errors"
	"strconv"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr error
	}{
		{"3", 3, nil},
		{" 12 ", 12, nil},
		{"0", 0, ErrNonPositive},
		{"-1", 0, ErrNonPositive},
		{"x", 0, strconv.ErrSyntax},
	}
	for _, c := range cases {
		got, err := ParseQuantity(c.in)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("ParseQuantity(%q) err = %v, want %v", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseQuantity(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}
