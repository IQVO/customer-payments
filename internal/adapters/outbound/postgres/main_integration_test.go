//go:build integration

package postgres_test

import (
	"os"
	"testing"

	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}
