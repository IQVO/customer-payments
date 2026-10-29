package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

var (
	_ ports.Clock       = (*Clock)(nil)
	_ ports.IDGenerator = (*SequentialIDs)(nil)
)

// Clock is a settable ports.Clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock reading t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current reading.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// SequentialIDs is a ports.IDGenerator minting pay-1, pay-2, ... and ref-1,
// ref-2, ... in order.
type SequentialIDs struct {
	mu   sync.Mutex
	a, r int
}

// NewAuthorizationID returns the next authorization id.
func (g *SequentialIDs) NewAuthorizationID() paymentauthorization.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.a++
	return paymentauthorization.ID(fmt.Sprintf("pay-%d", g.a))
}

// NewRefundID returns the next refund id.
func (g *SequentialIDs) NewRefundID() refund.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.r++
	return refund.ID(fmt.Sprintf("ref-%d", g.r))
}
