package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// DefaultPolicyInterval is how often the clock-driven policies run.
const DefaultPolicyInterval = 5 * time.Second

// policyRunner is one clock-driven policy pass.
type policyRunner struct {
	name string
	run  func(ctx context.Context) (int, error)
}

// policyRunners are ReauthorizeBeforeExpiry, VoidUnboundAuthorizations, the
// refunds a provider outage left REQUESTED, and the housekeeping of the parked
// facts (escalation after 24 h, purge after 30 days).
func policyRunners(d usecases.Deps) []policyRunner {
	maintain := usecases.MaintainParkedFacts{Deps: d}
	return []policyRunner{
		{"reauthorize-before-expiry", usecases.ReauthorizeBeforeExpiry{Deps: d}.Execute},
		{"void-unbound-authorizations", usecases.VoidUnboundAuthorizations{Deps: d}.Execute},
		{"process-requested-refunds", usecases.ProcessRequestedRefunds{Deps: d}.Execute},
		{"maintain-parked-facts", func(ctx context.Context) (int, error) {
			m, err := maintain.Execute(ctx)
			return m.Escalated + m.Purged, err
		}},
	}
}

// runPolicies runs every policy once, logging failures; a failing pass is
// retried by the next tick.
func runPolicies(ctx context.Context, logger *slog.Logger, runners []policyRunner) {
	for _, r := range runners {
		n, err := r.run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("policy pass failed", "policy", r.name, "error", err)
			continue
		}
		if n > 0 {
			logger.Info("policy pass done", "policy", r.name, "handled", n)
		}
	}
}

// startPolicies starts the always-on policy loop.
func startPolicies(d usecases.Deps, logger *slog.Logger) (*worker, func()) {
	interval := parsePolicyInterval(os.Getenv("POLICY_INTERVAL"), logger)
	runners := policyRunners(d)
	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "policies", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		logger.Info("policies running", "interval", interval)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			runPolicies(ctx, logger, runners)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return w, stop
}
