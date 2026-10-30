package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A missing DATABASE_URL is a boot error, never a silent in-memory fallback.
func TestBuildDeps_MissingDatabaseFailsBoot(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want an error naming DATABASE_URL", err)
	}
}

// run refuses to boot without DATABASE_URL, naming it, before anything else.
func TestRunRefusesToBootWithoutDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("A2A_AGENT_URL", testAgentURL)
	if err := run(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() without DATABASE_URL = %v, want a boot error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error after the bounded retries.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// buildDeps threads MIGRATIONS_DATABASE_URL (not DATABASE_URL) into the boot
// migrations. A cancelled context would lose the real cause (the error wraps
// only ctx.Err()), so this runs with a short timeout and asserts on the
// "retrying ... err=" line the first failed attempt logs: it must carry the
// migrations URL's own parse error, not a dial error against DATABASE_URL.
func TestBuildDeps_RunsMigrationsAgainstMigrationsDatabaseURL(t *testing.T) {
	const (
		bogusMigrationsURL = "not-a-valid-connection-string"
		unreachableAppURL  = "postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, _, err := buildDeps(ctx, logger, unreachableAppURL, bogusMigrationsURL); err == nil {
		t.Fatal("a malformed MIGRATIONS_DATABASE_URL must fail boot")
	}
	out := logs.String()
	if !strings.Contains(out, "retrying") || !strings.Contains(out, "run migrations") || !strings.Contains(out, "parse scheme") {
		t.Fatalf("the first migration attempt did not fail on the migrations URL's parse error: %q", out)
	}
	if strings.Contains(out, "connection refused") {
		t.Fatalf("migrations ran against DATABASE_URL instead of MIGRATIONS_DATABASE_URL: %q", out)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("CP_A2A_TEST_SET", "v")
	t.Setenv("CP_A2A_TEST_EMPTY", "")
	if got := getenv("CP_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("CP_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
