package http

import "net/http"

// CatalogueRow is one problem type of the catalogue as the replay-class tests read it.
type CatalogueRow struct {
	Err    error
	Slug   string
	Status int
	Class  string
}

// Catalogue exposes every entry of problemCatalogue with the replay class it declares
// (empty when it declares none).
func Catalogue() []CatalogueRow {
	rows := make([]CatalogueRow, 0, len(problemCatalogue))
	for _, e := range problemCatalogue {
		rows = append(rows, CatalogueRow{Err: e.err, Slug: e.p.slug, Status: e.p.status, Class: string(e.replay)})
	}
	return rows
}

// WriteError is writeError for the tests of the Idempotency-Key middleware over a store:
// it answers err as its problem, so the replay class of the problem marks the capturing
// writer the way a handler's answer does.
func WriteError(w http.ResponseWriter, r *http.Request, err error) { writeError(w, r, err) }
