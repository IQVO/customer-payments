package refund

import (
	"errors"
	"fmt"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// Snapshot is the persisted form of a Refund. Repositories store it and
// Restore rebuilds the aggregate from it, validating every invariant.
type Snapshot struct {
	Request          Request
	State            State
	Attempts         int
	FailureCode      string
	ProviderRefundID string
	CreatedAt        time.Time
	Version          int64
}

// Snapshot returns the persisted form of the aggregate.
func (r *Refund) Snapshot() Snapshot {
	return Snapshot{
		Request: r.req, State: r.state, Attempts: r.attempts, FailureCode: r.failureCode,
		ProviderRefundID: r.providerRefundID, CreatedAt: r.createdAt, Version: r.version,
	}
}

// Restore rebuilds a persisted refund, validating every invariant that does
// not depend on other aggregates.
func Restore(s Snapshot) (*Refund, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("refund %q: %v", s.Request.ID, err)
	}
	return &Refund{
		req: s.Request, state: s.State, attempts: s.Attempts, failureCode: s.FailureCode,
		providerRefundID: s.ProviderRefundID, createdAt: shared.UTC(s.CreatedAt), version: s.Version, loaded: s.Version,
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if err := checkRequest(s.Request); err != nil {
		return err
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	if s.Attempts < 0 || s.Attempts > MaxAttempts {
		return fmt.Errorf("attempts %d is outside 0 to %d", s.Attempts, MaxAttempts)
	}
	switch s.State {
	case StateRequested:
		return nil
	case StateSucceeded:
		if s.ProviderRefundID == "" {
			return errors.New("a succeeded refund needs the provider's refund id")
		}
	case StateFailed:
		if s.FailureCode == "" {
			return errors.New("a failed refund needs a failure code")
		}
	default:
		return fmt.Errorf("unknown state %q", s.State)
	}
	return nil
}
