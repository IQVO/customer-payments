package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/application/provider"
)

var _ ports.PaymentProvider = (*Provider)(nil)

// Call is one recorded provider call.
type Call struct {
	Operation   string
	Key         string
	HoldID      string
	AmountMinor int64
}

// Provider is a scriptable ports.PaymentProvider double. Like a real
// provider it is idempotent on the key: a repeated key returns the first
// answer without a second effect. Fail makes the next calls of an operation
// return an error (nil clears it).
type Provider struct {
	mu    sync.Mutex
	clock ports.Clock
	// Validity is how long an approved hold lasts (default 7 days).
	Validity time.Duration
	calls    []Call
	answers  map[string]string
	fails    map[string]error
	seq      int
}

// NewProvider returns a provider whose holds expire Validity after the clock.
func NewProvider(clock ports.Clock) *Provider {
	return &Provider{clock: clock, Validity: 7 * 24 * time.Hour, answers: map[string]string{}, fails: map[string]error{}}
}

// Fail makes every call of the operation (authorize, capture, void, refund,
// reauthorize, reduce) return err until it is set to nil.
func (p *Provider) Fail(operation string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		delete(p.fails, operation)
		return
	}
	p.fails[operation] = err
}

// Calls returns the calls that reached the provider, oldest first, including
// replays of a key.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// Effects returns how many distinct keys of the operation took effect.
func (p *Provider) Effects(operation string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for k := range p.answers {
		if len(k) > len(operation) && k[:len(operation)+1] == operation+"|" {
			n++
		}
	}
	return n
}

func (p *Provider) do(op, key, hold string, amount int64, mint func() string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, Call{Operation: op, Key: key, HoldID: hold, AmountMinor: amount})
	if err := p.fails[op]; err != nil {
		return "", err
	}
	if a, ok := p.answers[op+"|"+key]; ok {
		return a, nil
	}
	p.seq++
	a := mint()
	p.answers[op+"|"+key] = a
	return a, nil
}

func (p *Provider) id(prefix string) func() string {
	return func() string { return fmt.Sprintf("%s-%d", prefix, p.seq) }
}

// Authorize implements ports.PaymentProvider.
func (p *Provider) Authorize(_ context.Context, req provider.AuthorizeRequest) (provider.Hold, error) {
	id, err := p.do("authorize", req.Key, "", req.AmountMinor, p.id("hold"))
	if err != nil {
		return provider.Hold{}, err
	}
	return provider.Hold{ID: id, ExpiresAt: p.clock.Now().Add(p.Validity)}, nil
}

// Capture implements ports.PaymentProvider.
func (p *Provider) Capture(_ context.Context, key, holdID string, amountMinor int64) (string, error) {
	return p.do("capture", key, holdID, amountMinor, p.id("cap"))
}

// Void implements ports.PaymentProvider.
func (p *Provider) Void(_ context.Context, key, holdID string) error {
	_, err := p.do("void", key, holdID, 0, p.id("void"))
	return err
}

// Refund implements ports.PaymentProvider.
func (p *Provider) Refund(_ context.Context, key, holdID string, amountMinor int64) (string, error) {
	return p.do("refund", key, holdID, amountMinor, p.id("prf"))
}

// Reauthorize implements ports.PaymentProvider.
func (p *Provider) Reauthorize(_ context.Context, key, holdID string, amountMinor int64) (provider.Hold, error) {
	id, err := p.do("reauthorize", key, holdID, amountMinor, p.id("hold"))
	if err != nil {
		return provider.Hold{}, err
	}
	return provider.Hold{ID: id, ExpiresAt: p.clock.Now().Add(p.Validity)}, nil
}

// ReduceAuthorization implements ports.PaymentProvider.
func (p *Provider) ReduceAuthorization(_ context.Context, key, holdID string, amountMinor int64) error {
	_, err := p.do("reduce", key, holdID, amountMinor, p.id("red"))
	return err
}
