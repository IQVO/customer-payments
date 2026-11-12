package bdd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/memory"
	"github.com/IQVO/customer-payments/internal/application/provider"
	"github.com/IQVO/customer-payments/internal/application/usecases"
)

// steps_wave2_test.go — the wave-2 vocabulary: raw REST requests (explicit
// Idempotency-Key for the replay/reuse paths), dotted response-field
// assertions, the settable clock, provider failure programming and the H2 /
// unbound-void maintenance policies.

func registerWave2Steps(sc *godog.ScenarioContext, get func() *world) {
	registerRawSteps(sc, get)
	registerPolicySteps(sc, get)
}

// registerRawSteps registers the raw request and response steps.
func registerRawSteps(sc *godog.ScenarioContext, get func() *world) {
	sc.Step(`^a GET request is sent to "([^"]+)"$`, func(path string) error {
		return get().do(http.MethodGet, path, "")
	})
	sc.Step(`^a POST request is sent to "([^"]+)"$`, func(path string) error {
		return get().do(http.MethodPost, path, "")
	})
	sc.Step(`^a POST request is sent to "([^"]+)" with key "([^"]+)" and the JSON body (.*)$`,
		func(path, key, body string) error { return get().doKey(http.MethodPost, path, key, body) })

	sc.Step(`^the response header "([^"]+)" is "([^"]+)"$`, func(name, want string) error {
		if got := get().header.Get(name); got != want {
			return fmt.Errorf("header %s = %q, want %q", name, got, want)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]+)" is (.+)$`, func(path, want string) error {
		v, ok := get().field(path)
		if !ok {
			return fmt.Errorf("no field %q in body: %s", path, get().body)
		}
		if got := scalar(v); got != want {
			return fmt.Errorf("field %q = %q, want %q (body: %s)", path, got, want, get().body)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" has (\d+) items$`, func(path string, want int) error {
		v, ok := get().field(path)
		if !ok {
			return fmt.Errorf("no list %q in body: %s", path, get().body)
		}
		list, isList := v.([]any)
		if !isList {
			return fmt.Errorf("field %q is not a list: %s", path, get().body)
		}
		if len(list) != want {
			return fmt.Errorf("list %q has %d items, want %d", path, len(list), want)
		}
		return nil
	})
}

// registerPolicySteps registers the clock, provider-failure and policy-run
// steps.
func registerPolicySteps(sc *godog.ScenarioContext, get func() *world) {
	sc.Step(`^time advances (\d+) hours$`, func(n int) error {
		get().clock().Advance(time.Duration(n) * time.Hour)
		return nil
	})
	sc.Step(`^the provider "([^"]+)" is failing with (DECLINED|UNAVAILABLE)(?: code (\S+))?$`,
		func(op, kind, code string) error {
			if kind == "DECLINED" {
				if code == "" {
					code = "DECLINED"
				}
				get().provider.Fail(op, provider.Declined(code))
				return nil
			}
			get().provider.Fail(op, provider.ErrUnavailable)
			return nil
		})
	sc.Step(`^the provider recovers$`, func() error {
		for _, op := range []string{"authorize", "capture", "void", "refund", "reauthorize", "reduce"} {
			get().provider.Fail(op, nil)
		}
		return nil
	})
	sc.Step(`^the provider "([^"]+)" ran (\d+) times$`, func(op string, want int) error {
		if got := get().provider.Effects(op); got != want {
			return fmt.Errorf("provider %q ran %d times, want %d", op, got, want)
		}
		return nil
	})
	sc.Step(`^the reauthorization policy runs$`, func() error {
		n, err := (usecases.ReauthorizeBeforeExpiry{Deps: get().deps}).Execute(context.Background())
		get().handled, get().policyErr = n, err
		return get().policyErr
	})
	sc.Step(`^the unbound-void policy runs$`, func() error {
		n, err := (usecases.VoidUnboundAuthorizations{Deps: get().deps}).Execute(context.Background())
		get().handled, get().policyErr = n, err
		return get().policyErr
	})
	sc.Step(`^(\d+) authorizations? (?:was|were) handled$`, func(want int) error {
		if get().handled != want {
			return fmt.Errorf("handled = %d, want %d", get().handled, want)
		}
		return nil
	})
}

// clock returns the scenario's settable clock.
func (w *world) clock() *memory.Clock { return w.deps.Clock.(*memory.Clock) }

// field reads a dotted path ("a.b.0.c") of the last response body.
func (w *world) field(path string) (any, bool) {
	var v any
	if json.Unmarshal([]byte(w.body), &v) != nil {
		return nil, false
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// scalar renders a decoded JSON value the way the feature files write it.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
