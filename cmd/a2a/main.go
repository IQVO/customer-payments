// Command a2a is the composition root for the customer-payments Agent2Agent
// (A2A) agent (docs/adr/0005-a2a-inbound-adapter.md): it wires env config to the
// outbound Postgres repositories, the repositories to the SAME read use cases
// the REST and MCP adapters use (GetAuthorization, ListAuthorizations,
// GetRefund, ListRefunds), and those to the inbound A2A adapter, then serves
// A2A JSON-RPC over HTTP with the Agent Card at the well-known path. It is a
// third, independent deployable alongside cmd/api and cmd/mcp.
//
// The surface is READ-ONLY, like cmd/mcp: no write use case is wired here at
// all, so this binary can never insert into the outbox. Its outbox publisher
// is therefore not even built: it never starts the outbox relay, never runs the
// consumers and the policies, never dials Kafka (no KAFKA_BROKERS, no consumer
// groups) and never calls the payment provider: those run in cmd/api only. It
// makes no REST, MCP or A2A call to any sibling context and holds no outbound
// A2A client.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// A missing or malformed mandatory variable is a boot error naming it, raised
// before telemetry, any connection and the listener.
//
// Environment:
//
//	A2A_ADDR                 listen address (default :8093)
//	A2A_AGENT_URL            REQUIRED. Absolute http(s) URL of this agent's JSON-RPC endpoint, advertised on the Agent Card
//	DATABASE_URL             REQUIRED. Postgres DSN (the service database cmd/api owns)
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	LOG_LEVEL                debug | info | warn | error (default info)
//	OTEL_SERVICE_NAME        service.name of spans and metrics (default customer-payments-a2a)
//	OTEL_EXPORTER_OTLP_ENDPOINT  Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION          service.version, reported at boot too (default dev)
//	ENVIRONMENT              deployment.environment.name, reported at boot too (default production in the boot line, local in telemetry)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inbounda2a "github.com/IQVO/customer-payments/internal/adapters/inbound/a2a"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/telemetry"
	"github.com/IQVO/customer-payments/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errNoDatabase is the boot error of a missing DATABASE_URL: this binary has
// no in-memory mode.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// errNoAgentURL is the boot error of a missing A2A_AGENT_URL: the Agent Card
// must advertise the absolute URL clients dial (ADR 0005).
var errNoAgentURL = errors.New("A2A_AGENT_URL is required: the Agent Card must advertise the absolute URL of this agent's JSON-RPC endpoint (ADR 0005), e.g. http://<release>-a2a.<ns>.svc.cluster.local:8093/")

// bootConfig is the mandatory configuration of cmd/a2a: real Postgres and the
// URL the Agent Card advertises, with no default and no degraded mode behind
// either.
type bootConfig struct {
	databaseURL string
	agentURL    string
}

// checkAgentURL validates A2A_AGENT_URL: an absolute http(s) URL. The card is
// the discovery contract; a relative or schemeless URL is a misconfiguration
// every client would hit, so it is refused at boot, never silently served.
func checkAgentURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errNoAgentURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("A2A_AGENT_URL=%q is not an absolute http(s) URL: the Agent Card must advertise the endpoint clients dial (ADR 0005)", raw)
	}
	return nil
}

// loadBootConfig reads and validates the mandatory environment. Every problem
// is reported at once, each naming its variable, so the process fails fast at
// boot, before telemetry, any connection and the listener.
func loadBootConfig() (bootConfig, error) {
	cfg := bootConfig{
		databaseURL: os.Getenv("DATABASE_URL"),
		agentURL:    strings.TrimSpace(os.Getenv("A2A_AGENT_URL")),
	}
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, errNoDatabase)
	}
	if err := checkAgentURL(cfg.agentURL); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return bootConfig{}, errors.Join(errs...)
	}
	return cfg, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("a2a agent exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Fail fast, before telemetry, any connection and the listener.
	cfg, err := loadBootConfig()
	if err != nil {
		return err
	}

	// Telemetry comes up before the router is built (otelchimetric binds the
	// global MeterProvider at construction) and is flushed last.
	tel := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, tel)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	deps, closeAdapters, err := buildDeps(context.Background(), logger, cfg.databaseURL, getenv("MIGRATIONS_DATABASE_URL", cfg.databaseURL))
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("A2A_ADDR", ":8093"),
		Handler:           newAgent(deps, cfg.agentURL, getenv("SERVICE_VERSION", "dev"), logger, tel.serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("a2a agent starting", "version", getenv("SERVICE_VERSION", "dev"), "environment", getenv("ENVIRONMENT", "production"))
	return serveA2A(logger, srv)
}

// buildDeps migrates the database, connects a pgx pool and wires the four
// read use cases over the Postgres repositories (no unit of work, no outbox, no
// publisher, no payment provider: the agent is read-only). The returned close func
// releases the pool. An empty databaseURL is a boot error: there is no
// fallback runtime.
//
// Like cmd/api this binary runs the idempotent embedded migrations on start,
// so it can boot against a fresh database before cmd/api has; golang-
// migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN
// where DATABASE_URL points at PgBouncer, which cannot honour the
// session-scoped advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string) (inbounda2a.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inbounda2a.Deps{}, noop, errNoDatabase
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inbounda2a.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inbounda2a.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	return inbounda2a.NewDeps(postgres.NewAuthorizationRepository(pool), postgres.NewRefundRepository(pool)), pool.Close, nil
}

// serveA2A runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveA2A(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("a2a agent listening (JSON-RPC over HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info. The handler is wrapped so a
// record written with a span-carrying context also carries trace_id/span_id.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
