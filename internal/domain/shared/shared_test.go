package shared_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestInvalidWrapsErrValidation(t *testing.T) {
	err := shared.Invalid("field %s is %d", "x", 3)
	if !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("Invalid() = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "field x is 3") {
		t.Errorf("Invalid() = %q, want the formatted cause", err)
	}
}

func TestCorruptWrapsErrInvalidSnapshot(t *testing.T) {
	err := shared.Corrupt("row %q", "a")
	if !errors.Is(err, shared.ErrInvalidSnapshot) {
		t.Fatalf("Corrupt() = %v, want ErrInvalidSnapshot", err)
	}
	if errors.Is(err, shared.ErrValidation) {
		t.Error("a corrupt snapshot must not be a client validation error")
	}
	if !strings.Contains(err.Error(), `row "a"`) {
		t.Errorf("Corrupt() = %q, want the formatted cause", err)
	}
}

func TestValidID(t *testing.T) {
	long := strings.Repeat("a", shared.MaxID)
	cases := []struct {
		id   string
		want bool
	}{
		{"pay-100234", true},
		{"A", true},
		{"9", true},
		{"a.b_c:d-e", true},
		{long, true},
		{long + "a", false},
		{"", false},
		{"-a", false},
		{".a", false},
		{"_a", false},
		{":a", false},
		{"a b", false},
		{"a/b", false},
		{"a\x00", false},
		{"é", false},
		{"a@", false},
		{"a`", false},
		{"a{", false},
		{"A[", false},
		{"Z", true},
		{"z", true},
		{"0", true},
		{"@", false},
		{"/", false},
		{":", false},
	}
	for _, c := range cases {
		if got := shared.ValidID(c.id); got != c.want {
			t.Errorf("ValidID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestUTCTruncatesToMicrosecondsAndKeepsZero(t *testing.T) {
	if got := shared.UTC(time.Time{}); !got.IsZero() {
		t.Errorf("UTC(zero) = %v, want zero", got)
	}
	loc := time.FixedZone("x", -3*3600)
	in := time.Date(2026, 10, 24, 9, 0, 0, 123456789, loc)
	got := shared.UTC(in)
	want := time.Date(2026, 10, 24, 12, 0, 0, 123456000, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("UTC() = %v, want %v in UTC", got, want)
	}
}

func TestHeader(t *testing.T) {
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	h := shared.Header{Version: 7, At: at}
	if h.AggregateVersion() != 7 || !h.OccurredAt().Equal(at) {
		t.Errorf("Header = (%d, %v), want (7, %v)", h.AggregateVersion(), h.OccurredAt(), at)
	}
}

func TestValidCurrency(t *testing.T) {
	for c, want := range map[string]bool{"BRL": true, "USD": true, "AAA": true, "ZZZ": true, "brl": false, "BR": false, "BRLL": false, "": false, "B1L": false, "@RL": false, "BR[": false} {
		if got := shared.ValidCurrency(c); got != want {
			t.Errorf("ValidCurrency(%q) = %v, want %v", c, got, want)
		}
	}
}

func TestCheckAmount(t *testing.T) {
	if err := shared.CheckAmount(0); err != nil {
		t.Errorf("CheckAmount(0) = %v", err)
	}
	if err := shared.CheckAmount(shared.MaxMinor); err != nil {
		t.Errorf("CheckAmount(max) = %v", err)
	}
	if err := shared.CheckAmount(-1); !errors.Is(err, shared.ErrNegativeAmount) {
		t.Errorf("CheckAmount(-1) = %v, want ErrNegativeAmount", err)
	}
	if err := shared.CheckAmount(shared.MaxMinor + 1); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("CheckAmount(max+1) = %v, want ErrValidation", err)
	}
}
