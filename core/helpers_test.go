package core

import "testing"

func coreTestPtr[T any](value T) *T { return &value }

func coreTestMinor(t *testing.T, amount string) int64 {
	t.Helper()
	minor, err := ToMinorUnits(amount, "price")
	if err != nil {
		t.Fatalf("ToMinorUnits(%q): %v", amount, err)
	}
	return minor
}

func coreTestSumMinor(t *testing.T, items []BasketItem) int64 {
	t.Helper()
	var total int64
	for _, item := range items {
		total += coreTestMinor(t, item.Price)
	}
	return total
}
