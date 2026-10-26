// Package ids is the production ports.IDGenerator: every id is a prefix and 12
// random hexadecimal characters.
package ids

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/IQVO/customer-payments/internal/application/ports"
	"github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
)

// Generator mints ids.
type Generator struct{}

var _ ports.IDGenerator = Generator{}

func random(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic("ids: system randomness is unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

// NewAuthorizationID returns "pay-" and 12 hexadecimal characters.
func (Generator) NewAuthorizationID() paymentauthorization.ID {
	return paymentauthorization.ID(random("pay-"))
}

// NewRefundID returns "ref-" and 12 hexadecimal characters.
func (Generator) NewRefundID() refund.ID { return refund.ID(random("ref-")) }
