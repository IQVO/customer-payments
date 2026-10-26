package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	httpadapter "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
)

func readyzStatus(t *testing.T, g *httpadapter.Readiness) (int, string) {
	t.Helper()
	srv := httptest.NewServer(httpadapter.NewRouter(&httpadapter.Server{Readiness: g}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

func TestReadyzNamesTheFailingDependency(t *testing.T) {
	down := httpadapter.Dependency{Name: "kafka", Check: func(context.Context) error { return errors.New("no broker") }}
	up := httpadapter.Dependency{Name: "postgres", Check: func(context.Context) error { return nil }}
	status, body := readyzStatus(t, httpadapter.NewReadiness(0, 0, up, down))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", status, body)
	}
	if !strings.Contains(body, "/problems/dependency-unavailable") || !strings.Contains(body, "kafka") || strings.Contains(body, "postgres") {
		t.Fatalf("body = %s", body)
	}
}

func TestReadyzIsReadyWhenDependenciesPass(t *testing.T) {
	up := httpadapter.Dependency{Name: "postgres", Check: func(context.Context) error { return nil }}
	if status, body := readyzStatus(t, httpadapter.NewReadiness(0, 0, up)); status != http.StatusOK || !strings.Contains(body, "ready") {
		t.Fatalf("status = %d: %s", status, body)
	}
}

func TestReadinessCachesTheVerdictForTheTTL(t *testing.T) {
	var calls atomic.Int32
	dep := httpadapter.Dependency{Name: "postgres", Check: func(context.Context) error { calls.Add(1); return nil }}
	g := httpadapter.NewReadiness(time.Hour, time.Second, dep)
	for range 3 {
		if f := g.Failing(context.Background()); f != nil {
			t.Fatalf("failing = %v", f)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("checks = %d, want 1 within the ttl", calls.Load())
	}
}

func TestNilReadinessIsReady(t *testing.T) {
	var g *httpadapter.Readiness
	g.SetNotReady()
	if !g.Ready() || g.Failing(context.Background()) != nil {
		t.Fatal("a nil gate must be ready")
	}
}
