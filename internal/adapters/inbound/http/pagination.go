package http

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/inbound/http/page"
	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/application/views"
)

// errUnknownQueryParameter marks a query parameter a collection GET does not
// declare (STANDARD 4.2): a consumer still sending a retired paging parameter
// (`after`, `from`, `offset`, `nextToken`) fails loudly instead of reading page
// one forever.
var errUnknownQueryParameter = errors.New("unknown query parameter")

// pageParams reads the `limit` and `cursor` query parameters of a collection GET
// and rejects any other query parameter than those and the declared filters.
// The cursor is the opaque page.Position whose sort key is the created-at
// instant of the last returned row (RFC 3339, nanoseconds) and whose id is its
// id; a cursor that does not decode to that is invalid-cursor.
func pageParams(r *http.Request, filters ...string) (limit int, after *pagination.Keyset, err error) {
	if name := page.Unknown(r, filters...); name != "" {
		return 0, nil, fmt.Errorf("%w: %s", errUnknownQueryParameter, name)
	}
	if limit, err = page.Limit(r); err != nil {
		return 0, nil, err
	}
	pos, err := page.After(r)
	if err != nil || pos == nil {
		return limit, nil, err
	}
	at, perr := time.Parse(time.RFC3339Nano, pos.Sort)
	if perr != nil {
		return 0, nil, fmt.Errorf("%w: %v", page.ErrInvalidCursor, perr)
	}
	return limit, &pagination.Keyset{CreatedAt: at, ID: pos.ID}, nil
}

// pageOut renders one use case page as the {items,nextCursor} envelope.
func pageOut[V, B any](items []V, next *pagination.Keyset, body func(V) B) page.Page[B] {
	out := page.Page[B]{Items: make([]B, len(items))}
	for i, v := range items {
		out.Items[i] = body(v)
	}
	if next != nil {
		out.NextCursor = page.Encode(page.Position{Sort: next.CreatedAt.UTC().Format(time.RFC3339Nano), ID: next.ID})
	}
	return out
}

func authorizationPageOut(items []views.Authorization, next *pagination.Keyset) page.Page[authorizationOut] {
	return pageOut(items, next, authorizationFrom)
}

func refundPageOut(items []views.Refund, next *pagination.Keyset) page.Page[refundOut] {
	return pageOut(items, next, refundFrom)
}
