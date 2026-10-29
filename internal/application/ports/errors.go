package ports

import "errors"

var (
	// ErrNotFound is what every repository returns for a Get or Find that
	// matches nothing (problem type not-found, 404). An adapter wraps its own
	// miss (no rows, a missing map key) into this sentinel.
	ErrNotFound = errors.New("not found")
	// ErrConcurrentUpdate is what Save returns when the stored state is not the
	// one the aggregate was loaded at (problem type concurrent-update, 409):
	// the expectation is `stored version == aggregate.LoadedVersion()`, and a
	// create (LoadedVersion 0) expects that the id is not stored yet and that
	// no other live authorization exists for the checkout session.
	ErrConcurrentUpdate = errors.New("concurrent update")
)
