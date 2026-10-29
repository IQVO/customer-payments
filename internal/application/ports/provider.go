package ports

import (
	"context"

	"github.com/IQVO/customer-payments/internal/application/provider"
)

// PaymentProvider is the Anti-Corruption Layer port to the payment provider.
// Every call carries a deterministic idempotency Key (see usecases.Key), so a
// repeated call returns the first answer. It never receives a card number,
// only the PSP token. A definitive refusal is Declined(code); a provider that
// cannot be reached or timed out is provider.ErrUnavailable.
type PaymentProvider interface {
	// Authorize holds amountMinor on the payment method.
	Authorize(ctx context.Context, req provider.AuthorizeRequest) (provider.Hold, error)
	// Capture takes amountMinor of the hold and returns the capture id.
	Capture(ctx context.Context, key, holdID string, amountMinor int64) (string, error)
	// Void releases the whole remaining hold.
	Void(ctx context.Context, key, holdID string) error
	// Refund returns amountMinor of captured money and returns the refund id.
	Refund(ctx context.Context, key, holdID string, amountMinor int64) (string, error)
	// Reauthorize renews the hold for amountMinor and returns the new one.
	Reauthorize(ctx context.Context, key, holdID string, amountMinor int64) (provider.Hold, error)
	// ReduceAuthorization lowers the remaining hold to amountMinor, the
	// partial release of H4.
	ReduceAuthorization(ctx context.Context, key, holdID string, amountMinor int64) error
}
