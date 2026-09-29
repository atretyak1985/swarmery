// Package orders models customer orders: their line items, money arithmetic,
// status lifecycle, pricing from a catalog, and a printable summary.
package orders

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Money is an amount in cents. Negative amounts are credits.
type Money int64

// Cents builds a Money from whole units and cents, e.g. Cents(12, 34) is 12.34.
func Cents(units, cents int64) Money {
	return Money(units*100 + cents)
}

// Add returns m + n.
func (m Money) Add(n Money) Money {
	return m + n
}

// Mul returns m multiplied by a whole quantity.
func (m Money) Mul(qty int) Money {
	return m * Money(qty)
}

// Percent returns pct percent of m, rounded half away from zero to the cent.
func (m Money) Percent(pct int) Money {
	return Money(roundDiv(int64(m)*int64(pct), 100))
}

// BasisPoints returns bp hundredths of a percent of m, rounded half away from zero.
func (m Money) BasisPoints(bp int) Money {
	return Money(roundDiv(int64(m)*int64(bp), 10000))
}

// String formats m as a decimal amount with two places, e.g. "12.34" or "-0.05".
func (m Money) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// roundDiv divides a by b (b > 0) rounding half away from zero.
func roundDiv(a, b int64) int64 {
	if a < 0 {
		return -((-a + b/2) / b)
	}
	return (a + b/2) / b
}

// Status is the lifecycle state of an order.
type Status string

// The states an order moves through.
const (
	StatusDraft     Status = "draft"
	StatusPlaced    Status = "placed"
	StatusShipped   Status = "shipped"
	StatusDelivered Status = "delivered"
	StatusCancelled Status = "cancelled"
)

// transitions lists, for each status, the statuses it may move to.
var transitions = map[Status][]Status{
	StatusDraft:   {StatusPlaced, StatusCancelled},
	StatusPlaced:  {StatusShipped, StatusCancelled},
	StatusShipped: {StatusDelivered},
}

// CanTransition reports whether an order in status from may move to status to.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Terminal reports whether no further transition is possible from s.
func (s Status) Terminal() bool {
	return len(transitions[s]) == 0
}

// Errors returned by this package.
var (
	ErrEmptyOrder    = errors.New("orders: order has no line items")
	ErrInvalidQty    = errors.New("orders: quantity must be positive")
	ErrUnknownSKU    = errors.New("orders: unknown sku")
	ErrBadTransition = errors.New("orders: status transition not allowed")
	ErrNotEditable   = errors.New("orders: order is no longer editable")
)

// LineItem is one SKU on an order, bought Qty times at UnitPrice each.
type LineItem struct {
	SKU       string
	Qty       int
	UnitPrice Money
}

// Subtotal is Qty times UnitPrice.
func (li LineItem) Subtotal() Money {
	return li.UnitPrice.Mul(li.Qty)
}

// Catalog maps a SKU to its current unit price.
type Catalog map[string]Money

// Price returns the unit price of sku, or ErrUnknownSKU.
func (c Catalog) Price(sku string) (Money, error) {
	p, ok := c[sku]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownSKU, sku)
	}
	return p, nil
}

// Order is a customer order. The zero value is not usable; call NewOrder.
type Order struct {
	ID          string
	Status      Status
	Items       []LineItem
	DiscountPct int // whole-order discount, 0..100
	TaxRateBP   int // tax rate in basis points, applied after the discount
}

// NewOrder returns an empty draft order with the given ID.
func NewOrder(id string) *Order {
	return &Order{ID: id, Status: StatusDraft}
}

// editable returns ErrNotEditable unless the order is still a draft.
func (o *Order) editable() error {
	if o.Status != StatusDraft {
		return fmt.Errorf("%w: status %s", ErrNotEditable, o.Status)
	}
	return nil
}

// find returns the index of the line item for sku, or -1.
func (o *Order) find(sku string) int {
	for i, li := range o.Items {
		if li.SKU == sku {
			return i
		}
	}
	return -1
}

// AddItem adds qty of sku priced from the catalog. Adding a SKU already on the
// order increases its quantity and keeps its original unit price.
func (o *Order) AddItem(c Catalog, sku string, qty int) error {
	if err := o.editable(); err != nil {
		return err
	}
	if qty <= 0 {
		return ErrInvalidQty
	}
	if i := o.find(sku); i >= 0 {
		o.Items[i].Qty += qty
		return nil
	}
	price, err := c.Price(sku)
	if err != nil {
		return err
	}
	o.Items = append(o.Items, LineItem{SKU: sku, Qty: qty, UnitPrice: price})
	return nil
}

// SetQty sets the quantity of sku. A quantity of zero removes the line item.
func (o *Order) SetQty(sku string, qty int) error {
	if err := o.editable(); err != nil {
		return err
	}
	if qty < 0 {
		return ErrInvalidQty
	}
	i := o.find(sku)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrUnknownSKU, sku)
	}
	if qty == 0 {
		o.Items = append(o.Items[:i], o.Items[i+1:]...)
		return nil
	}
	o.Items[i].Qty = qty
	return nil
}

// RemoveItem removes the line item for sku.
func (o *Order) RemoveItem(sku string) error {
	return o.SetQty(sku, 0)
}

// Subtotal is the sum of every line item's subtotal.
func (o *Order) Subtotal() Money {
	var sum Money
	for _, li := range o.Items {
		sum = sum.Add(li.Subtotal())
	}
	return sum
}

// Discount is the whole-order discount amount.
func (o *Order) Discount() Money {
	return o.Subtotal().Percent(o.DiscountPct)
}

// Tax is the tax on the discounted subtotal.
func (o *Order) Tax() Money {
	return (o.Subtotal() - o.Discount()).BasisPoints(o.TaxRateBP)
}

// Total is the subtotal minus the discount plus tax.
func (o *Order) Total() Money {
	return o.Subtotal() - o.Discount() + o.Tax()
}

// Validate reports the first problem that would stop the order being placed.
func (o *Order) Validate() error {
	if len(o.Items) == 0 {
		return ErrEmptyOrder
	}
	for _, li := range o.Items {
		if li.Qty <= 0 {
			return fmt.Errorf("%w: %s", ErrInvalidQty, li.SKU)
		}
	}
	if o.DiscountPct < 0 || o.DiscountPct > 100 {
		return fmt.Errorf("orders: discount %d%% out of range", o.DiscountPct)
	}
	return nil
}

// Transition moves the order to status to, validating it first when placing.
func (o *Order) Transition(to Status) error {
	if !CanTransition(o.Status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrBadTransition, o.Status, to)
	}
	if to == StatusPlaced {
		if err := o.Validate(); err != nil {
			return err
		}
	}
	o.Status = to
	return nil
}

// SortedItems returns a copy of the line items ordered by SKU.
func (o *Order) SortedItems() []LineItem {
	items := append([]LineItem(nil), o.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].SKU < items[j].SKU })
	return items
}

// ItemCount is the total quantity across all line items.
func (o *Order) ItemCount() int {
	n := 0
	for _, li := range o.Items {
		n += li.Qty
	}
	return n
}

// Summary renders the order as a fixed-width text receipt, items sorted by SKU.
func (o *Order) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Order %s (%s)\n", o.ID, o.Status)
	for _, li := range o.SortedItems() {
		fmt.Fprintf(&b, "  %-10s %3d x %8s = %9s\n", li.SKU, li.Qty, li.UnitPrice, li.Subtotal())
	}
	fmt.Fprintf(&b, "  %-27s %9s\n", "subtotal", o.Subtotal())
	if o.DiscountPct > 0 {
		fmt.Fprintf(&b, "  %-27s %9s\n", fmt.Sprintf("discount %d%%", o.DiscountPct), -o.Discount())
	}
	if o.TaxRateBP > 0 {
		fmt.Fprintf(&b, "  %-27s %9s\n", "tax", o.Tax())
	}
	fmt.Fprintf(&b, "  %-27s %9s\n", "total", o.Total())
	return b.String()
}
