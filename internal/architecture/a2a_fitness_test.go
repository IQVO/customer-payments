package architecture

// A2A inbound adapter sensors (docs/adr/0005). The A2A agent is an ADDITIVE,
// READ-ONLY inbound adapter, like the MCP server (its charter: "Nothing here
// changes data"): it can read authorizations and refunds and can never
// authorize, void, capture or refund, emit an event, call the payment
// provider, or call another context. Pinned structurally, not by review:
//
//   - the adapter depends only on the application and domain layers and
//     nothing else depends on it (TestA2AAdapterDependencyRule);
//   - an ALLOWLIST of usecases.X identifiers: the adapter's and cmd/a2a's
//     non-test sources may name the four read use cases (GetAuthorization,
//     ListAuthorizations, GetRefund, ListRefunds) and the Deps bundle they
//     embed, nothing else - so no write use case and no consumer/policy;
//   - the adapter's own Deps struct holds those four read use cases only;
//   - no write-capable seam is imported (outbox, payment provider, Kafka,
//     the memory doubles, the REST adapter, the outbound adapters);
//   - no outbound A2A client anywhere in the non-test sources (this context
//     calls no sibling), and the A2A SDK is imported by the adapter and
//     cmd/a2a only.
//
// TestA2AReadOnlySensorBites proves each detector flags what it should, from
// code fixtures (comments never count: the checks are AST based).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

// a2aUseCases is the whole set of use cases the A2A surface may hold or wire.
var a2aUseCases = map[string]bool{"GetAuthorization": true, "ListAuthorizations": true, "GetRefund": true, "ListRefunds": true}

// a2aAllowedUsecaseRefs are the names of package usecases the A2A sources may
// refer to: the four read use cases and Deps, the shared ports bundle they
// EMBED (NewDeps sets only the two read repositories; every write seam of the
// bundle - unit of work, outbox, payment provider - stays unnamed). Everything
// else in package usecases is a write path or a seam to one.
var a2aAllowedUsecaseRefs = map[string]bool{
	"GetAuthorization": true, "ListAuthorizations": true, "GetRefund": true, "ListRefunds": true, "Deps": true,
}

// a2aForbiddenImportParts are import-path fragments no A2A source may carry:
// the write-capable seams (transactional outbox, payment provider ACL, Kafka
// adapters, the in-memory doubles, the REST adapter, any other outbound
// adapter except the Postgres repositories and telemetry cmd/a2a wires) plus
// the outbound A2A client.
var a2aForbiddenImportParts = []string{
	"a2a-go/v2/a2aclient",
	"/internal/application/outbox",
	"/internal/application/provider",
	"/internal/application/idempotency",
	"/internal/application/parked",
	"/internal/adapters/outbound/outbox",
	"/internal/adapters/outbound/paymentprovider",
	"/internal/adapters/outbound/kafka",
	"/internal/adapters/outbound/memory",
	"/internal/adapters/inbound/kafka",
	"/internal/adapters/inbound/http",
	"segmentio/kafka-go",
}

// TestA2AAdapterDependencyRule encodes ADR 0005: the A2A inbound adapter is
// additive, never load-bearing. It may depend only on the application and
// domain layers (never on outbound adapters, never on cmd), and nothing else
// in this codebase may depend on it; cmd/a2a wires it up and nothing else
// touches it.
func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.adapters.inbound.a2a.**",
					ShouldOnlyDependsOn: &configuration.Dependencies{
						Internal: []string{
							"**.internal.adapters.inbound.a2a.**",
							"**.internal.application.**",
							"**.internal.domain.**",
						},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.**",
					ShouldNotDependsOn: &configuration.Dependencies{
						Internal: []string{"**.internal.adapters.inbound.a2a.**"},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})
}

// a2aImportViolations returns one finding per forbidden import of one source.
func a2aImportViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		for _, part := range a2aForbiddenImportParts {
			if strings.Contains(path, part) {
				out = append(out, fmt.Sprintf("%s imports %s", filename, path))
			}
		}
	}
	return out, nil
}

// usecaseName returns N for the type expression *usecases.N.
func usecaseName(e ast.Expr) (string, bool) {
	star, ok := e.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "usecases" {
		return "", false
	}
	return sel.Sel.Name, true
}

// a2aSDKImporters returns the sources that import the A2A SDK.
func a2aSDKImporters(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		if strings.Contains(imp.Path.Value, "a2aproject/a2a-go") {
			out = append(out, filename)
		}
	}
	return out, nil
}

// a2aDepsViolations returns every field of the struct named Deps in src whose
// type is not *usecases.<one of the two A2A read use cases>.
func a2aDepsViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Deps" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if name, ok := usecaseName(field.Type); ok && a2aUseCases[name] {
				continue
			}
			names := make([]string, 0, len(field.Names))
			for _, id := range field.Names {
				names = append(names, id.Name)
			}
			out = append(out, fmt.Sprintf("%s: Deps field %s is not an A2A read use case", filename, strings.Join(names, ",")))
		}
		return false
	})
	return out, nil
}

// a2aUsecaseRefViolations returns every usecases.<Name> a source refers to
// that is not in a2aAllowedUsecaseRefs (the A2A sources must not even name a
// write use case or GetShipTo).
func a2aUsecaseRefViolations(filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "usecases" && !a2aAllowedUsecaseRefs[sel.Sel.Name] {
			out = append(out, fmt.Sprintf("%s refers to usecases.%s, which is not one of the four A2A read use cases", filename, sel.Sel.Name))
		}
		return true
	})
	return out, nil
}

func a2aSources(t *testing.T) (adapter, command []string) {
	t.Helper()
	adapter = goFilesUnder(t, "../adapters/inbound/a2a", false)
	command = goFilesUnder(t, filepath.Join("..", "..", "cmd", "a2a"), false)
	if len(adapter) == 0 || len(command) == 0 {
		t.Fatalf("no non-test Go file under the A2A adapter (%d) or cmd/a2a (%d): the sensor would pass vacuously", len(adapter), len(command))
	}
	sort.Strings(adapter)
	sort.Strings(command)
	return adapter, command
}

// TestA2AServerIsReadOnlyAndHasNoOutboundClient: the A2A adapter and cmd/a2a
// can reach no write path and hold no outbound A2A client.
func TestA2AServerIsReadOnlyAndHasNoOutboundClient(t *testing.T) {
	adapter, command := a2aSources(t)
	all := append(append([]string{}, adapter...), command...)

	for _, path := range all {
		found, err := a2aImportViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- the A2A surface may read through the use cases only and calls no sibling (docs/adr/0005)", v)
		}
		refs, err := a2aUsecaseRefViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range refs {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- wire only the four read use cases (docs/adr/0005)", v)
		}
	}
	for _, path := range adapter {
		found, err := a2aDepsViolations(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, v := range found {
			t.Errorf("READ-ONLY A2A VIOLATION: %s -- the A2A adapter may hold the four read use cases only (docs/adr/0005)", v)
		}
	}
}

// TestA2ASDKIsImportedByTheAdapterAndItsCommandOnly keeps the SDK (and the
// protocol it carries) out of the domain, the application and every other
// adapter: only the adapter and its composition root speak A2A.
func TestA2ASDKIsImportedByTheAdapterAndItsCommandOnly(t *testing.T) {
	allowed := func(path string) bool {
		slash := filepath.ToSlash(path)
		return strings.Contains(slash, "/adapters/inbound/a2a/") || strings.Contains(slash, "cmd/a2a/")
	}
	var scanned int
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		for _, path := range goFilesUnder(t, root, false) {
			scanned++
			found, err := a2aSDKImporters(path, nil)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, importer := range found {
				if !allowed(importer) {
					t.Errorf("%s imports the A2A SDK; only internal/adapters/inbound/a2a and cmd/a2a may (docs/adr/0005)", importer)
				}
			}
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d sources: the sensor is looking in the wrong place", scanned)
	}
}

// TestA2AReadOnlySensorBites proves each detector flags what it should.
func TestA2AReadOnlySensorBites(t *testing.T) {
	cases := []struct {
		name string
		run  func(filename string, src any) ([]string, error)
		src  string
		want int
	}{
		{"an outbound a2a client is flagged", a2aImportViolations, "package x\nimport \"github.com/a2aproject/a2a-go/v2/a2aclient\"", 1},
		{"the outbox port is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/application/outbox\"", 1},
		{"the payment provider port is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/application/provider\"", 1},
		{"the payment provider adapter is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/adapters/outbound/paymentprovider\"", 1},
		{"the outbox adapter is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/adapters/outbound/outbox\"", 1},
		{"a kafka consumer is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/adapters/inbound/kafka\"", 1},
		{"the kafka client is flagged", a2aImportViolations, "package x\nimport \"github.com/segmentio/kafka-go\"", 1},
		{"the REST adapter is flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/adapters/inbound/http\"", 1},
		{"the memory doubles are flagged", a2aImportViolations, "package x\nimport \"github.com/IQVO/customer-payments/internal/adapters/outbound/memory\"", 1},
		{"the server SDK package passes", a2aImportViolations, "package x\nimport \"github.com/a2aproject/a2a-go/v2/a2asrv\"", 0},
		{"usecases and ports pass", a2aImportViolations, "package x\nimport (\n\"github.com/IQVO/customer-payments/internal/application/usecases\"\n\"github.com/IQVO/customer-payments/internal/application/ports\"\n)", 0},

		{"a write use case field is flagged", a2aDepsViolations, "package x\ntype Deps struct{ Authorize *usecases.AuthorizePayment }", 1},
		{"a refund write field is flagged", a2aDepsViolations, "package x\ntype Deps struct{ Refund *usecases.RequestRefund }", 1},
		{"a unit of work field is flagged", a2aDepsViolations, "package x\ntype Deps struct{ UoW ports.UnitOfWork }", 1},
		{"a non-pointer read use case is flagged", a2aDepsViolations, "package x\ntype Deps struct{ Get usecases.GetRefund }", 1},
		{"the four read use cases pass", a2aDepsViolations, "package x\ntype Deps struct{\nA *usecases.GetAuthorization\nB *usecases.ListAuthorizations\nC *usecases.GetRefund\nD *usecases.ListRefunds\n}", 0},
		{"other structs are not Deps", a2aDepsViolations, "package x\ntype Other struct{ W *usecases.VoidAuthorization }", 0},

		{"constructing a write use case is flagged", a2aUsecaseRefViolations, "package main\nvar _ = &usecases.AuthorizePayment{}", 1},
		{"naming the consumer is flagged", a2aUsecaseRefViolations, "package main\nvar _ *usecases.Consumer", 1},
		{"a mention in a comment is not a reference", a2aUsecaseRefViolations, "package main\n// usecases.AuthorizePayment is REST-only\nvar _ = 1", 0},
		{"the read use cases pass", a2aUsecaseRefViolations, "package main\nvar _ = &usecases.GetRefund{}\nvar _ = &usecases.ListRefunds{Deps: usecases.Deps{}}", 0},

		{"the SDK import is found", a2aSDKImporters, "package x\nimport \"github.com/a2aproject/a2a-go/v2/a2asrv\"", 1},
		{"other imports are not", a2aSDKImporters, "package x\nimport \"net/http\"", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.run("src.go", tc.src)
			if err != nil || len(got) != tc.want {
				t.Fatalf("findings = %v (err %v), want %d", got, err, tc.want)
			}
		})
	}
}
