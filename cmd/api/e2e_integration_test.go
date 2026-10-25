//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/testing/kafkatest"
	"github.com/IQVO/customer-payments/internal/testing/pgtest"
)

// The end-to-end proof that the shipped binary is really wired: the test boots
// the REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it only
// through REST and Kafka. Every Kafka read is bounded by a deadline.

const (
	topicEvents   = "retailnetwork.customer-payments.events"
	topicCheckout = "retailnetwork.checkout.events"
	topicSourcing = "retailnetwork.order-sourcing.events"
	topicShipping = "retailnetwork.shipping-and-tracking.events"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	kafkatest.Shutdown()
	os.Exit(code)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

type service struct {
	base string
	stop func() error
}

func boot(t *testing.T, env map[string]string) *service {
	t.Helper()
	addr := freeAddr(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv("HTTP_ADDR", addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	var (
		once    sync.Once
		stopErr error
	)
	s := &service{base: "http://" + addr, stop: func() error {
		once.Do(func() {
			cancel()
			select {
			case stopErr = <-done:
			case <-time.After(60 * time.Second):
				stopErr = fmt.Errorf("the service did not stop")
			}
		})
		return stopErr
	}}
	t.Cleanup(func() { _ = s.stop() })
	eventually(t, 90*time.Second, "readyz to report ready", func() bool {
		resp, err := http.Get(s.base + "/readyz")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == 200
	})
	return s
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

type reply struct {
	status int
	body   string
}

func (s *service) call(t *testing.T, method, path, key, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: string(b)}
}

func (r reply) field(t *testing.T, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not JSON: %s", r.body)
	}
	return m[name]
}

// cloudEvent builds a structured-mode CloudEvents message for a producer's
// topic; subject (also the Kafka key) is the aggregate id of the producer.
func cloudEvent(id, typ, subject string, data map[string]any) kafkatest.Message {
	b, _ := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": "/retailnetwork/test", "type": typ, "subject": subject,
		"time": time.Now().UTC().Format(time.RFC3339Nano), "datacontenttype": "application/json", "data": data,
	})
	return kafkatest.Message{Key: subject, Value: b}
}
