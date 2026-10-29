package ids_test

import (
	"regexp"
	"testing"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/ids"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestIDsHaveTheirPrefixAndAreValidAndUnique(t *testing.T) {
	rx := regexp.MustCompile(`^(pay|ref)-[0-9a-f]{12}$`)
	g := ids.Generator{}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		for _, id := range []string{string(g.NewAuthorizationID()), string(g.NewRefundID())} {
			if !rx.MatchString(id) || !shared.ValidID(id) || seen[id] {
				t.Fatalf("bad or repeated id %q", id)
			}
			seen[id] = true
		}
	}
}
