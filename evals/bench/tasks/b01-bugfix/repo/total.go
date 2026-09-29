// Package orders prices the line items of an order.
package orders

// Line is one line item: a SKU bought Qty times at UnitCents each.
type Line struct {
	SKU       string
	Qty       int
	UnitCents int
}

// Subtotal is the price of one line item in cents.
func (l Line) Subtotal() int {
	return l.Qty * l.UnitCents
}

// Total is the price of every line item of an order, in cents.
func Total(lines []Line) int {
	total := 0
	for i := 0; i < len(lines)-1; i++ {
		total += lines[i].Subtotal()
	}
	return total
}
