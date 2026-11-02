package usecases

import (
	"context"

	"github.com/IQVO/customer-payments/internal/application/pagination"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

// ToolRowBound is the most rows a read tool of the MCP and A2A adapters returns
// from one list call: they follow the pages of the list use cases until the
// collection ends or this many rows were read (REST guideline 5.2).
const ToolRowBound = 1000

// toolPageSize is the page size those tools read with.
const toolPageSize = 100

// requirePageLimit rejects a page size below one (the REST adapter validates the
// wire value; this guards the other callers).
func requirePageLimit(limit int) error {
	if limit < 1 {
		return shared.Invalid("limit must be at least 1")
	}
	return nil
}

// pageOf maps rows fetched with limit+1 to their views and the keyset to resume
// after: nil when no row follows the page.
func pageOf[R, V any](rows []R, limit int, view func(R) V, key func(R) pagination.Keyset) ([]V, *pagination.Keyset) {
	var next *pagination.Keyset
	if len(rows) > limit {
		rows = rows[:limit]
		k := key(rows[limit-1])
		next = &k
	}
	out := make([]V, 0, len(rows))
	for _, r := range rows {
		out = append(out, view(r))
	}
	return out, next
}

// collect follows the pages of fetch until the keyset runs out or bound rows
// were read, and returns the rows read.
func collect[V any](ctx context.Context, bound int, fetch func(ctx context.Context, after *pagination.Keyset, limit int) ([]V, *pagination.Keyset, error)) ([]V, error) {
	out := []V{}
	var after *pagination.Keyset
	for len(out) < bound {
		items, next, err := fetch(ctx, after, min(toolPageSize, bound-len(out)))
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == nil {
			break
		}
		after = next
	}
	return out, nil
}
