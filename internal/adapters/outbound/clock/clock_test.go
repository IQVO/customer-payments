package clock_test

import (
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/clock"
)

func TestSystemClockIsUTCAndMicrosecondPrecise(t *testing.T) {
	before := time.Now().UTC().Truncate(time.Microsecond)
	got := clock.System{}.Now()
	after := time.Now().UTC()
	if got.Location() != time.UTC || got.Nanosecond()%1000 != 0 || got.Before(before) || got.After(after) {
		t.Fatalf("now = %v (before %v after %v)", got, before, after)
	}
}
