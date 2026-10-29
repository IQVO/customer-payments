// Package clock is the production ports.Clock.
package clock

import (
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
)

// System reads the wall clock, in UTC, truncated to the microsecond (ADR 0002,
// A17: Postgres stores microseconds, so a round trip must not change a timestamp).
type System struct{}

var _ ports.Clock = System{}

// Now returns the current instant.
func (System) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
