package shared

import (
	"errors"
	"fmt"
)

// ErrNegativeAmount: every amount is zero or more (422 negative-amount).
var ErrNegativeAmount = errors.New("negative amount")

// MaxMinor is the largest amount in minor units (ADR 0002, A6), a plain
// literal on purpose: it keeps every sum of allocations inside an int64.
const MaxMinor = 1000000000

// CheckAmount reports why n is not a valid amount in minor units:
// ErrNegativeAmount, or ErrValidation above MaxMinor.
func CheckAmount(n int64) error {
	if n < 0 {
		return fmt.Errorf("%w: %d", ErrNegativeAmount, n)
	}
	if n > MaxMinor {
		return Invalid("amount %d is above the maximum of %d minor units", n, MaxMinor)
	}
	return nil
}

// ValidCurrency reports whether c is three upper-case ASCII letters.
func ValidCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}
