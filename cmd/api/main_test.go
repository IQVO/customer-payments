package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func fullEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "a:9092, b:9092", "PAYMENT_PROVIDER_URL": "http://provider:8080",
		"CHECKOUT_CONSUMER_GROUP": "g1", "ORDER_SOURCING_CONSUMER_GROUP": "g2", "SHIPPING_AND_TRACKING_CONSUMER_GROUP": "g3",
	}
}

func TestLoadRuntimeAcceptsAFullEnvironment(t *testing.T) {
	rt, err := loadRuntime(envOf(fullEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if rt.databaseURL != "postgres://x" || len(rt.brokers) != 2 || rt.brokers[1] != "b:9092" ||
		rt.providerURL != "http://provider:8080" || rt.groups["CHECKOUT_CONSUMER_GROUP"] != "g1" ||
		rt.groups["ORDER_SOURCING_CONSUMER_GROUP"] != "g2" || rt.groups["SHIPPING_AND_TRACKING_CONSUMER_GROUP"] != "g3" || len(rt.groups) != 3 {
		t.Fatalf("runtime = %+v", rt)
	}
}

func TestLoadRuntimeNamesTheMissingVariable(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "KAFKA_BROKERS", "PAYMENT_PROVIDER_URL", "CHECKOUT_CONSUMER_GROUP", "ORDER_SOURCING_CONSUMER_GROUP", "SHIPPING_AND_TRACKING_CONSUMER_GROUP"} {
		env := fullEnv()
		delete(env, name)
		_, err := loadRuntime(envOf(env))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("without %s: %v", name, err)
		}
	}
	env := fullEnv()
	env["KAFKA_BROKERS"] = " , "
	if _, err := loadRuntime(envOf(env)); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("blank brokers: %v", err)
	}
}

func TestParseRelayInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": time.Second, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "0.5": 500 * time.Millisecond, "junk": time.Second, "-3s": time.Second, "0": time.Second} {
		if got := parseRelayInterval(raw, quiet()); got != want {
			t.Fatalf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestOutboxMaxAttempts(t *testing.T) {
	for raw, want := range map[string]int{"": 20, "5": 5, "0": 0, "\t7": 7, "-1": 20, "many": 20} {
		if got := outboxMaxAttempts(raw, quiet()); got != want {
			t.Fatalf("%q = %d, want %d", raw, got, want)
		}
	}
}

func TestParsePolicyInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": DefaultPolicyInterval, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "junk": DefaultPolicyInterval, "-3s": DefaultPolicyInterval, "0": DefaultPolicyInterval} {
		if got := parsePolicyInterval(raw, quiet()); got != want {
			t.Fatalf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": DefaultShutdownDrainDelay, "0": 0, "0s": 0, "2s": 2 * time.Second, "-1s": DefaultShutdownDrainDelay, "x": DefaultShutdownDrainDelay} {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(quiet()); got != want {
			t.Fatalf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "info": slog.LevelInfo, "": slog.LevelInfo, "loud": slog.LevelInfo} {
		l := newLogger(level)
		for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
			if got := l.Enabled(context.Background(), lvl); got != (lvl >= want) {
				t.Fatalf("level %q: enabled(%v) = %v", level, lvl, got)
			}
		}
	}
	if getenv("CUSTOMER_PAYMENTS_UNSET", "fallback") != "fallback" {
		t.Fatal("getenv fallback")
	}
	t.Setenv("CUSTOMER_PAYMENTS_SET", "v")
	if getenv("CUSTOMER_PAYMENTS_SET", "fallback") != "v" {
		t.Fatal("getenv value")
	}
	if joinBrokers([]string{"a:1", "b:2"}) != "a:1,b:2" {
		t.Fatal("joinBrokers")
	}
}

func TestReadinessNeedsPostgresAndKafka(t *testing.T) {
	g := newReadiness(func(context.Context) error { return errors.New("down") }, []string{"127.0.0.1:1"})
	failing := g.Failing(context.Background())
	if len(failing) != 2 || failing[0] != "postgres" || failing[1] != "kafka" {
		t.Fatalf("failing = %v", failing)
	}
}

func TestWatchWorkersReportsADeadWorker(t *testing.T) {
	alive, dead := &worker{name: "alive", done: make(chan struct{})}, &worker{name: "dead", done: make(chan struct{})}
	close(dead.done)
	select {
	case name := <-watchWorkers(nil, alive, dead):
		if name != "dead" {
			t.Fatalf("died = %s", name)
		}
	case <-time.After(time.Second):
		t.Fatal("no worker reported")
	}
	close(alive.done)
}

func TestAwaitIsBoundedAndNilSafe(t *testing.T) {
	await(quiet(), nil)
	done := &worker{name: "w", done: make(chan struct{})}
	close(done.done)
	await(quiet(), done)
}

func serveOnce(t *testing.T, ctx context.Context, consumers []*worker, relay, policies *worker) (*bytes.Buffer, *inboundhttp.Readiness, error) {
	t.Helper()
	var buf bytes.Buffer
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	readiness := inboundhttp.NewReadiness(0, 0)
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	closed := 0
	err := serveUntilSignal(ctx, slog.New(slog.NewTextHandler(&buf, nil)), srv, readiness, consumers, func() { closed++ }, relay, func() { closed++ }, policies, func() { closed++ })
	if closed == 0 {
		t.Fatal("the workers must be closed on every exit path")
	}
	return &buf, readiness, err
}

func TestServeUntilSignalShutsDownCleanlyWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	consumer, relay := &worker{name: "c", done: make(chan struct{})}, &worker{name: "relay", done: make(chan struct{})}
	policies := &worker{name: "policies", done: make(chan struct{})}
	// The workers stop only AFTER the context ends, as on a real shutdown; closing
	// them first would look like a worker that died on its own.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		time.Sleep(50 * time.Millisecond)
		close(consumer.done)
		close(relay.done)
		close(policies.done)
	}()
	_, readiness, err := serveOnce(t, ctx, []*worker{consumer}, relay, policies)
	if err != nil || readiness.Ready() {
		t.Fatalf("err = %v ready = %v (readiness must flip first)", err, readiness.Ready())
	}
}

func TestServeUntilSignalExitsWhenAWorkerDies(t *testing.T) {
	relay := &worker{name: "outbox-relay", done: make(chan struct{})}
	close(relay.done)
	_, _, err := serveOnce(t, context.Background(), nil, relay, &worker{name: "policies", done: make(chan struct{})})
	if err == nil || !strings.Contains(err.Error(), "outbox-relay stopped unexpectedly") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeUntilSignalReturnsAListenError(t *testing.T) {
	taken := httptest.NewServer(http.NotFoundHandler())
	defer taken.Close()
	srv := &http.Server{Addr: strings.TrimPrefix(taken.URL, "http://"), Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	relay := &worker{name: "relay", done: make(chan struct{})}
	err := serveUntilSignal(context.Background(), quiet(), srv, inboundhttp.NewReadiness(0, 0), nil, func() {}, relay, func() {}, &worker{name: "policies", done: make(chan struct{})}, func() {})
	if err == nil {
		t.Fatal("binding a taken port must fail")
	}
}
