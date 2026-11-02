package usecases

import (
	"context"
	"errors"
	"testing"

	"github.com/IQVO/customer-payments/internal/application/pagination"
)

// collect stops at the bound (a page never asks for more rows than are left under it), at the
// last page, and on an error.
func TestCollectStopsAtTheBoundTheLastPageAndErrors(t *testing.T) {
	var asked []int
	endless := func(_ context.Context, _ *pagination.Keyset, limit int) ([]int, *pagination.Keyset, error) {
		asked = append(asked, limit)
		return make([]int, limit), &pagination.Keyset{ID: "more"}, nil
	}
	got, err := collect(context.Background(), 250, endless)
	if err != nil || len(got) != 250 || len(asked) != 3 || asked[0] != 100 || asked[2] != 50 {
		t.Fatalf("bounded: %d rows, pages asked %v, err %v", len(got), asked, err)
	}

	last := func(context.Context, *pagination.Keyset, int) ([]int, *pagination.Keyset, error) {
		return []int{1, 2}, nil, nil
	}
	if got, err := collect(context.Background(), 250, last); err != nil || len(got) != 2 {
		t.Fatalf("last page: %v %v", got, err)
	}

	boom := errors.New("boom")
	failing := func(context.Context, *pagination.Keyset, int) ([]int, *pagination.Keyset, error) {
		return nil, nil, boom
	}
	if _, err := collect(context.Background(), 250, failing); !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}

// pageOf cuts the extra row off and points the keyset at the last returned row.
func TestPageOfPointsAfterTheLastReturnedRow(t *testing.T) {
	key := func(s string) pagination.Keyset { return pagination.Keyset{ID: s} }
	id := func(s string) string { return s }
	items, next := pageOf([]string{"a", "b", "c"}, 2, id, key)
	if len(items) != 2 || next == nil || next.ID != "b" {
		t.Fatalf("more rows follow: %v %v", items, next)
	}
	items, next = pageOf([]string{"a", "b"}, 2, id, key)
	if len(items) != 2 || next != nil {
		t.Fatalf("exactly limit rows is the last page: %v %v", items, next)
	}
	if items, next = pageOf[string, string](nil, 2, id, key); items == nil || len(items) != 0 || next != nil {
		t.Fatalf("no rows is an empty, non-nil page: %v %v", items, next)
	}
}
