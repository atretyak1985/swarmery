// Package orders prices orders and applies discounts to them.
package orders

// Line is one line item: a SKU bought Qty times at UnitCents each.
type Line struct {
	SKU       string
	Qty       int
	UnitCents int
}

// Subtotal is the price of every line item, in cents.
func Subtotal(lines []Line) int {
	total := 0
	for _, l := range lines {
		total += l.Qty * l.UnitCents
	}
	return total
}

// ApplyDiscount subtracts a fixed discount from a subtotal, both in cents.
// A discount larger than the subtotal brings the total to zero, never below.
func ApplyDiscount(subtotalCents, discountCents int) int {
	if discountCents >= subtotalCents {
		return 0
	}
	return subtotalCents - discountCents
}
