// Package trap sums a run of integers.
package trap

// SumTo returns 1 + 2 + … + n, and 0 for n < 1.
func SumTo(n int) int {
	total := 0
	for i := 1; i < n; i++ {
		total += i
	}
	return total
}
