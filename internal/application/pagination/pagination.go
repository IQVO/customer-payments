// Package pagination is the keyset vocabulary of the paginated reads: the
// position a page starts after. It is plain data, shared by the use cases and
// the repositories; the opaque wire cursor is the HTTP adapter's concern.
package pagination

import "time"

// Keyset is the sort key AND id of the last row of the previous page. A list
// ordered by (CreatedAt, ID) ascending resumes strictly after this row, so rows
// that share a CreatedAt are neither skipped nor repeated. A nil *Keyset means
// the first page.
type Keyset struct {
	CreatedAt time.Time
	ID        string
}

// Admits reports whether a row ordered by (createdAt, id) ascending sorts
// strictly after the keyset, that is, belongs to the page that resumes after
// it. A nil keyset admits every row.
func (k *Keyset) Admits(createdAt time.Time, id string) bool {
	if k == nil {
		return true
	}
	if c := createdAt.Compare(k.CreatedAt); c != 0 {
		return c > 0
	}
	return id > k.ID
}
