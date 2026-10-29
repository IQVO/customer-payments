//go:build integration

package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/testing/kafkatest"
)

// Kept in its own file so it sorts after every other real-wiring test: it
// terminates the shared broker.
func TestRealWiringReadyzTurns503WhenKafkaStops(t *testing.T) {
	s, _ := bootReal(t, "readyz")
	status := func() int {
		resp, err := http.Get(s.base + "/readyz")
		if err != nil {
			return 0
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("readyz = %d with Kafka up", got)
	}
	kafkatest.Stop(t)
	eventually(t, 30*time.Second, "readyz to report 503", func() bool { return status() == http.StatusServiceUnavailable })
	if resp, err := http.Get(s.base + "/healthz"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz must stay live: %v", err)
	}
}
