package kafka_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IQVO/customer-payments/internal/adapters/kafka/cloudevents"
	"github.com/IQVO/customer-payments/internal/adapters/outbound/kafka"
	"github.com/IQVO/customer-payments/internal/application/outbox"
	pa "github.com/IQVO/customer-payments/internal/domain/paymentauthorization"
	"github.com/IQVO/customer-payments/internal/domain/refund"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 123456000, time.UTC)

const fixedID = "7a2c9e41-5f0b-4c3d-9e6a-1b8d4f0c2a55"

func hdr(v int64) shared.Header { return shared.Header{Version: v, At: t0} }

func authorized() pa.PaymentAuthorized {
	return pa.PaymentAuthorized{
		Header: hdr(1), AuthorizationID: "pay-1", CheckoutSessionID: "cs-1", Currency: "BRL", TotalMinor: 10000,
		Allocations: []pa.AllocationAmount{{GroupNo: 1, AmountMinor: 7000}, {GroupNo: 2, AmountMinor: 3000}},
		ExpiresAt:   t0.Add(7 * 24 * time.Hour),
	}
}

func captured() pa.PaymentCaptured {
	return pa.PaymentCaptured{Header: hdr(2), AuthorizationID: "pay-1", OrderID: "ord-1", GroupNo: 1, AmountMinor: 7000}
}

func released() pa.PaymentReleased {
	return pa.PaymentReleased{Header: hdr(2), AuthorizationID: "pay-1", OrderID: "ord-1", GroupNo: 2, AmountMinor: 3000, Reason: pa.ReasonCancelled}
}

func releasedUnbound() pa.PaymentReleased {
	return pa.PaymentReleased{Header: hdr(2), AuthorizationID: "pay-1", GroupNo: 2, AmountMinor: 3000, Reason: pa.ReasonVoided}
}

func expired() pa.PaymentAuthorizationExpired {
	return pa.PaymentAuthorizationExpired{Header: hdr(3), AuthorizationID: "pay-1", OrderID: "ord-1", ExpiredAt: t0}
}

func declined() pa.PaymentDeclined {
	return pa.PaymentDeclined{Header: hdr(1), AuthorizationID: "pay-1", CheckoutSessionID: "cs-1", Reason: "CARD_DECLINED"}
}

func reauthorized() pa.PaymentReauthorized {
	return pa.PaymentReauthorized{
		Header: hdr(3), AuthorizationID: "pay-1", OrderID: "ord-1", ExpiresAt: t0.Add(14 * 24 * time.Hour), ProviderAuthorizationID: "hold-2",
	}
}

func refundIssued() refund.RefundIssued {
	return refund.RefundIssued{
		Header: hdr(2), RefundID: "ref-1", OrderID: "ord-1", AuthorizationID: "pay-1", GroupNo: 1, AmountMinor: 7000, Reason: refund.ReasonLost,
	}
}

func refundIssuedManual() refund.RefundIssued {
	e := refundIssued()
	e.GroupNo, e.Reason = 0, refund.ReasonManual
	return e
}

func refundFailed() refund.RefundFailed {
	return refund.RefundFailed{
		Header: hdr(2), RefundID: "ref-1", OrderID: "ord-1", AuthorizationID: "pay-1", GroupNo: 1, Code: "ACCOUNT_CLOSED", Attempts: 5, Final: true,
	}
}

func encodeOne(t *testing.T, ev shared.Event) outbox.Message {
	t.Helper()
	msgs, err := (&kafka.Encoder{NewID: func() string { return fixedID }}).Encode(ev)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Encode: %v (%d messages)", err, len(msgs))
	}
	return msgs[0]
}

// assertGolden compares the CloudEvents bytes with the golden file EXACTLY.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatalf("%s differs from its golden file:\n got %s\nwant %s", name, got, want)
	}
}

func assertEnvelope(t *testing.T, msg outbox.Message, entity, name, subject string) {
	t.Helper()
	wantType := "com.retailnetwork.storefront.customer-payments." + entity + "." + name
	if msg.EventType != wantType || msg.EventID != fixedID || msg.Topic != kafka.TopicEvents {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Subject != subject || string(msg.Key) != subject {
		t.Fatalf("subject/key = %q %q", msg.Subject, msg.Key)
	}
	if msg.DataSchema != "urn:retailnetwork:customer-payments:events:"+name+":v1" {
		t.Fatalf("dataschema = %q", msg.DataSchema)
	}
	if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || msg.Headers[0].Value != cloudevents.MediaType {
		t.Fatalf("headers = %+v", msg.Headers)
	}
	e, err := cloudevents.Decode(msg.Value)
	if err != nil || e.Type() != wantType || e.Source() != "/retailnetwork/customer-payments" || e.ID() != fixedID {
		t.Fatalf("decode: %v %v", err, e)
	}
}
