// Package orders parses and prices order line items.
package orders

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNonPositive is returned for a quantity of zero or less.
var ErrNonPositive = errors.New("orders: quantity must be positive")

// ParseQuantity parses a positive whole quantity such as "3".
func ParseQuantity(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("orders: parse quantity %q: %w", s, err)
	}
	if n <= 0 {
		return 0, ErrNonPositive
	}
	return n, nil
}
