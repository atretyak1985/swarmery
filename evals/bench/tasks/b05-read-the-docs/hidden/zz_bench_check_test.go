package orders

// Check-owned test for bench task b05-read-the-docs. check.sh copies it into a
// scratch copy of the workdir; the model never sees it.

import (
	"strings"
	"testing"
)

func TestBenchCheckParseLineItem(t *testing.T) {
	ok := []struct {
		in   string
		want LineItem
	}{
		{"A-100:3", LineItem{SKU: "A-100", Qty: 3}},
		{"  B-7:12 \n", LineItem{SKU: "B-7", Qty: 12}},
	}
	for _, c := range ok {
		got, err := ParseLineItem(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseLineItem(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"A-100", ":3", "A-100:0", "A-100:-2", "A-100:x", ""} {
		_, err := ParseLineItem(in)
		if err == nil {
			t.Errorf("ParseLineItem(%q): want an error", in)
			continue
		}
		// CONTRIBUTING.md: every returned error starts with "orders: ". This
		// covers wrapped errors too (the outer message must carry the prefix);
		// how the error is built — own validation or a wrapped strconv error —
		// is the solution's choice.
		if !strings.HasPrefix(err.Error(), "orders: ") {
			t.Errorf("ParseLineItem(%q): error %q lacks the \"orders: \" prefix", in, err)
		}
	}
}
