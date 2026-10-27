// Command api is customer-payments' composition root: it wires env config into
// adapters, adapters into use cases, and use cases into the HTTP router, the
// checkout, order-sourcing and shipping-and-tracking consumers (ADR 0003), the
// clock-driven policies and the outbox relay, all on real Postgres and real
// Kafka: there is no in-memory mode, no log sink and no consumer that is off.
//
// Environment:
//
//	HTTP_ADDR                        listen address (default :8080)
//	DATABASE_URL                     Postgres DSN (REQUIRED)
//	MIGRATIONS_DATABASE_URL          direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS                    comma-separated brokers (REQUIRED: relay, consumers, /readyz)
//	PAYMENT_PROVIDER_URL             base URL of the payment provider API (REQUIRED)
//	CHECKOUT_CONSUMER_GROUP          stable group id of the checkout consumer (REQUIRED)
//	ORDER_SOURCING_CONSUMER_GROUP    stable group id of the order-sourcing consumer (REQUIRED)
//	SHIPPING_AND_TRACKING_CONSUMER_GROUP  stable group id of the shipping-and-tracking consumer (REQUIRED)
//	OUTBOX_RELAY_INTERVAL            relay poll interval (Go duration or seconds, default 1s)
//	POLICY_INTERVAL                  clock-driven policy pass interval (default 5s)
//	OUTBOX_MAX_ATTEMPTS              failures before the relay excludes a row (default 20, 0 = unlimited)
//	SHUTDOWN_DRAIN_DELAY             wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                        debug | info | warn | error (default info)
//	OTEL_SERVICE_NAME                service.name of spans and metrics (default customer-payments)
//	OTEL_EXPORTER_OTLP_ENDPOINT      Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION                  service.version (default dev)
//	ENVIRONMENT                      deployment.environment.name (default local)
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a metadata request (verdict cached 3s, bounded 2s); /healthz is
// liveness only. The process exits non-zero when the relay or a consumer
// returns on its own, so the orchestrator restarts it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"

	inboundhttp "github.com/IQVO/customer-payments/internal/adapters/inbound/http"
	inboundkafka "github.com/IQVO/customer-payments/internal/adapters/inbound/kafka"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/clock"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/ids"
	outboundkafka "github.com/IQVO/customer-payments/internal/adapters/outbound/kafka"
	outboxrelay "github.com/IQVO/customer-payments/internal/adapters/outbound/outbox"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/paymentprovider"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/postgres"
	"github.com/IQVO/customer-payments/internal/application/usecases"
	"github.com/IQVO/customer-payments/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for the consumers and the
// relay to return after their context is cancelled.
const workerDrainTimeout = 10 * time.Second

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping /readyz
// to not-ready, before closing the listener.
const DefaultShutdownDrainDelay = 5 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the integration test stops the real entry point that way).
func run(ctx context.Context) error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened or bound.
	rt, err := loadRuntime(os.Getenv)
	if err != nil {
		return err
	}

	// Telemetry comes up before the router is built (otelchimetric binds the
	// global MeterProvider at construction) and is flushed LAST: deferred before
	// everything below, so the final spans of the drain still leave the process.
	tel := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, tel)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain, the consumers and the relay have all stopped.
	ad, closeAdapters, err := buildAdapters(ctx, rt, logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	readiness := newReadiness(ad.pool.Ping, rt.brokers)
	httpServer := &http.Server{
		Addr:              getenv("HTTP_ADDR", ":8080"),
		Handler:           inboundhttp.NewRouter(buildServer(ad, readiness), logger, tel.serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumers, closeConsumers := startConsumers(ad, rt, logger)
	relay, closeRelay := startOutboxRelay(ad.outbox, rt.brokers, logger)
	policies, closePolicies := startPolicies(ad.deps, logger)
	return serveUntilSignal(ctx, logger, httpServer, readiness, consumers, closeConsumers, relay, closeRelay, policies, closePolicies)
}

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping and
// a Kafka broker answering a topic-less metadata request, cached 3s, bounded 2s.
// The ping is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
}

// adapters is the set of outbound adapters the composition root wires.
type adapters struct {
	deps        usecases.Deps
	idempotency *postgres.IdempotencyStore
	outbox      *postgres.OutboxRepo
	pool        *pgxpool.Pool
	writer      *kafkago.Writer
	clock       clock.System
	ids         ids.Generator
}

// buildAdapters wires the Postgres adapters, running the embedded migrations
// first.
func buildAdapters(ctx context.Context, rt runtimeEnv, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the LAST error is returned and the process refuses to boot.
	migrationsURL := getenv("MIGRATIONS_DATABASE_URL", rt.databaseURL)
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error { return postgres.RunMigrations(migrationsURL) }); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, rt.databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")

	outbox := postgres.NewOutboxRepo(pool, outboundkafka.NewEncoder())
	ad := adapters{pool: pool, outbox: outbox, idempotency: postgres.NewIdempotencyStore(pool)}
	client, err := paymentprovider.New(rt.providerURL, nil)
	if err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	ad.writer = outboundkafka.NewWriter(joinBrokers(rt.brokers))
	ad.deps = usecases.Deps{
		Authorizations: postgres.NewAuthorizationRepository(pool), Refunds: postgres.NewRefundRepository(pool),
		Parked: postgres.NewParkedFacts(pool), Inbox: postgres.NewInbox(pool), Outbox: outbox,
		UoW: postgres.NewUnitOfWork(pool), Clock: ad.clock, IDs: ad.ids, Provider: client,
		Escalator: outboundkafka.Escalator{W: ad.writer}, Log: logger,
	}
	return ad, func() {
		if err := ad.writer.Close(); err != nil {
			logger.Error("kafka escalation writer close failed", "error", err)
		}
		pool.Close()
	}, nil

}

func buildServer(ad adapters, readiness *inboundhttp.Readiness) *inboundhttp.Server {
	d := ad.deps
	return &inboundhttp.Server{
		AuthorizePayment: usecases.AuthorizePayment{Deps: d}, VoidAuthorization: usecases.VoidAuthorization{Deps: d},
		GetAuthorization: usecases.GetAuthorization{Deps: d}, ListAuthorizations: usecases.ListAuthorizations{Deps: d},
		RequestRefund: usecases.RequestRefund{Deps: d}, RetryRefund: usecases.RetryRefund{Deps: d},
		GetRefund: usecases.GetRefund{Deps: d}, ListRefunds: usecases.ListRefunds{Deps: d},
		IdempotencyStore: ad.idempotency, Clock: ad.clock, Readiness: readiness, ParkedFacts: d.Parked.Count,
	}
}

// worker is a started background loop and the channel closed once it returned.
type worker struct {
	name string
	done chan struct{}
}

// consumerSpec is one consumer: its group variable and how to build it once the
// group is resolved.
type consumerSpec struct {
	groupEnv string
	build    func(c usecases.Consumer, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer
}

func consumerSpecs() []consumerSpec {
	return []consumerSpec{
		{"CHECKOUT_CONSUMER_GROUP", func(c usecases.Consumer, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewCheckoutConsumer(brokers, group, c, logger)
		}},
		{"ORDER_SOURCING_CONSUMER_GROUP", func(c usecases.Consumer, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewOrderSourcingConsumer(brokers, group, c, logger)
		}},
		{"SHIPPING_AND_TRACKING_CONSUMER_GROUP", func(c usecases.Consumer, brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewShippingConsumer(brokers, group, c, logger)
		}},
	}
}

// startConsumers starts every consumer; they are always on. The readers dial
// lazily inside Run, so a broker outage never blocks boot.
func startConsumers(ad adapters, rt runtimeEnv, logger *slog.Logger) ([]*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	var (
		workers []*worker
		closers []func() error
	)
	for _, spec := range consumerSpecs() {
		group := rt.groups[spec.groupEnv]
		consumer := spec.build(usecases.Consumer{Deps: ad.deps}, rt.brokers, group, logger)
		w := &worker{name: consumer.Name, done: make(chan struct{})}
		workers = append(workers, w)
		closers = append(closers, consumer.Close)
		go func() {
			defer close(w.done)
			logger.Info("consumer running", "consumer", consumer.Name, "topic", consumer.Topic, "group_id", group, "brokers", rt.brokers)
			if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
				logger.Error("consumer stopped", "consumer", consumer.Name, "error", err)
			}
		}()
	}
	return workers, func() {
		stop()
		for _, closeFn := range closers {
			_ = closeFn()
		}
	}
}

// startOutboxRelay starts the always-on relay goroutine over the Kafka sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, func()) {
	writer := outboundkafka.NewWriter(joinBrokers(brokers))
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewRelay(store, outboundkafka.Sink{W: writer}, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "outbox-relay", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() {
			if err := writer.Close(); err != nil {
				logger.Error("kafka relay writer close failed", "error", err)
			}
		}()
		logger.Info("outbox relay running", "interval", interval, "max_attempts", maxAttempts, "topic", outboundkafka.TopicEvents)
		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("outbox relay stopped", "error", err)
		}
	}()
	return w, stop
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers and the policy loop, (5) stop and
// await the relay LAST so nothing committed above is stranded; the caller's
// deferred pool.Close runs after all of it.
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	consumers []*worker, closeConsumers func(), relay *worker, closeRelay func(), policies *worker, closePolicies func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	died := watchWorkers(append([]*worker{relay, policies}, consumers...)...)
	select {
	case err := <-errCh:
		closeConsumers()
		closePolicies()
		closeRelay()
		return err
	case name := <-died:
		// A consumer or the relay returned on its own (a lost broker, a failed
		// reader): the service cannot run without it and must not sit "alive"
		// with a dead worker, so exit non-zero and let the orchestrator restart it.
		closeConsumers()
		closePolicies()
		closeRelay()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", name)
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	closeConsumers()
	for _, w := range consumers {
		await(logger, w)
	}
	closePolicies()
	await(logger, policies)
	closeRelay()
	await(logger, relay)
	return err
}

// watchWorkers returns a channel that receives the name of every worker that
// returns. Workers only return when stopped or on an unrecoverable failure, so
// a value read while the service is serving means a worker died.
func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
}

// await waits (bounded) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}
