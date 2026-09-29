package orders

import (
	"errors"
	"testing"
)

var catalog = Catalog{"A-100": Cents(2, 50), "B-200": Cents(9, 99), "C-300": Cents(0, 5)}

func TestMoney(t *testing.T) {
	if got := Cents(12, 34).String(); got != "12.34" {
		t.Errorf("String = %q", got)
	}
	if got := Money(-5).String(); got != "-0.05" {
		t.Errorf("negative String = %q", got)
	}
	if got := Money(333).Percent(50); got != 167 {
		t.Errorf("Percent rounds half up: %d", got)
	}
	if got := Money(-333).Percent(50); got != -167 {
		t.Errorf("Percent rounds half away from zero: %d", got)
	}
	if got := Money(1000).BasisPoints(825); got != 83 {
		t.Errorf("BasisPoints = %d", got)
	}
}

func TestAddItemMergesAndPrices(t *testing.T) {
	o := NewOrder("o-1")
	for _, step := range []struct {
		sku string
		qty int
	}{{"A-100", 2}, {"B-200", 1}, {"A-100", 1}} {
		if err := o.AddItem(catalog, step.sku, step.qty); err != nil {
			t.Fatal(err)
		}
	}
	if len(o.Items) != 2 || o.Items[0].Qty != 3 {
		t.Fatalf("items = %+v", o.Items)
	}
	if err := o.AddItem(catalog, "Z-999", 1); !errors.Is(err, ErrUnknownSKU) {
		t.Errorf("unknown sku err = %v", err)
	}
	if err := o.AddItem(catalog, "A-100", 0); !errors.Is(err, ErrInvalidQty) {
		t.Errorf("zero qty err = %v", err)
	}
}

func TestTotals(t *testing.T) {
	o := NewOrder("o-2")
	_ = o.AddItem(catalog, "A-100", 4) // 10.00
	_ = o.AddItem(catalog, "B-200", 1) // 9.99
	o.DiscountPct = 10
	o.TaxRateBP = 825
	if o.Subtotal() != 1999 || o.Discount() != 200 || o.Tax() != 148 || o.Total() != 1947 {
		t.Fatalf("sub=%d disc=%d tax=%d total=%d", o.Subtotal(), o.Discount(), o.Tax(), o.Total())
	}
	want := "Order o-2 (draft)\n" +
		"  A-100        4 x     2.50 =     10.00\n" +
		"  B-200        1 x     9.99 =      9.99\n" +
		"  subtotal                        19.99\n" +
		"  discount 10%                    -2.00\n" +
		"  tax                              1.48\n" +
		"  total                           19.47\n"
	if got := o.Summary(); got != want {
		t.Errorf("Summary:\n%s\nwant:\n%s", got, want)
	}
}

func TestLifecycle(t *testing.T) {
	o := NewOrder("o-3")
	if err := o.Transition(StatusPlaced); !errors.Is(err, ErrEmptyOrder) {
		t.Fatalf("placing empty order err = %v", err)
	}
	_ = o.AddItem(catalog, "C-300", 3)
	if err := o.Transition(StatusPlaced); err != nil {
		t.Fatal(err)
	}
	if err := o.SetQty("C-300", 1); !errors.Is(err, ErrNotEditable) {
		t.Errorf("edit placed order err = %v", err)
	}
	if err := o.Transition(StatusDelivered); !errors.Is(err, ErrBadTransition) {
		t.Errorf("skip shipped err = %v", err)
	}
	_ = o.Transition(StatusShipped)
	_ = o.Transition(StatusDelivered)
	if !o.Status.Terminal() {
		t.Errorf("delivered should be terminal")
	}
}

func TestSetQtyAndCount(t *testing.T) {
	o := NewOrder("o-4")
	_ = o.AddItem(catalog, "A-100", 2)
	_ = o.AddItem(catalog, "B-200", 2)
	_ = o.SetQty("A-100", 5)
	if err := o.RemoveItem("B-200"); err != nil || len(o.Items) != 1 || o.ItemCount() != 5 {
		t.Fatalf("remove: err=%v items=%+v", err, o.Items)
	}
}
