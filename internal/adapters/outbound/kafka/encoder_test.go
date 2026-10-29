package kafka_test

import (
	"strings"
	"testing"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/kafka"
	"github.com/IQVO/customer-payments/internal/application/outbox"
	"github.com/IQVO/customer-payments/internal/domain/shared"
)

func TestGoldenAuthorizationEvents(t *testing.T) {
	cases := []struct {
		golden, entity, name string
		ev                   shared.Event
	}{
		{"PaymentAuthorized", "paymentauthorization", "PaymentAuthorized", authorized()},
		{"PaymentCaptured", "paymentauthorization", "PaymentCaptured", captured()},
		{"PaymentReleased", "paymentauthorization", "PaymentReleased", released()},
		{"PaymentReleasedUnbound", "paymentauthorization", "PaymentReleased", releasedUnbound()},
		{"PaymentAuthorizationExpired", "paymentauthorization", "PaymentAuthorizationExpired", expired()},
		{"PaymentDeclined", "paymentauthorization", "PaymentDeclined", declined()},
		{"PaymentReauthorized", "paymentauthorization", "PaymentReauthorized", reauthorized()},
	}
	for _, c := range cases {
		msg := encodeOne(t, c.ev)
		assertGolden(t, c.golden, msg.Value)
		assertEnvelope(t, msg, c.entity, c.name, "pay-1")
	}
}

func TestGoldenRefundEvents(t *testing.T) {
	cases := []struct {
		golden, name string
		ev           shared.Event
	}{
		{"RefundIssued", "RefundIssued", refundIssued()},
		{"RefundIssuedManual", "RefundIssued", refundIssuedManual()},
		{"RefundFailed", "RefundFailed", refundFailed()},
	}
	for _, c := range cases {
		msg := encodeOne(t, c.ev)
		assertGolden(t, c.golden, msg.Value)
		assertEnvelope(t, msg, "refund", c.name, "ref-1")
	}
}

func TestOrderLevelRefundOmitsGroupNo(t *testing.T) {
	if v := string(encodeOne(t, refundIssuedManual()).Value); strings.Contains(v, "groupNo") {
		t.Fatalf("groupNo must be absent on an order-level refund: %s", v)
	}
	if v := string(encodeOne(t, releasedUnbound()).Value); strings.Contains(v, "orderId") {
		t.Fatalf("orderId must be absent before binding: %s", v)
	}
}

type otherEvent struct{ shared.Header }

func (otherEvent) EventName() string { return "Other" }
func (otherEvent) Entity() string    { return "other" }
func (otherEvent) SubjectID() string { return "x" }

func TestEncodeMintsAnIDPerEventAndKeepsOrder(t *testing.T) {
	msgs, err := kafka.NewEncoder().Encode(authorized(), captured(), refundIssued())
	if err != nil || len(msgs) != 3 {
		t.Fatalf("Encode: %v (%d)", err, len(msgs))
	}
	if msgs[0].EventID == msgs[1].EventID || msgs[1].EventID == msgs[2].EventID || msgs[0].EventID == "" {
		t.Fatalf("ids must differ: %+v", msgs)
	}
	if !strings.HasSuffix(msgs[0].EventType, ".PaymentAuthorized") || !strings.HasSuffix(msgs[2].EventType, ".RefundIssued") {
		t.Fatalf("order lost: %+v", msgs)
	}
}

func TestEncodeRefusesAnUnpublishedEventAndAnEmptySubject(t *testing.T) {
	if _, err := kafka.NewEncoder().Encode(otherEvent{}); err == nil {
		t.Fatal("an event the service does not publish is a programming error")
	}
	msgs, err := (&kafka.Encoder{}).Encode()
	if err != nil || len(msgs) != 0 {
		t.Fatalf("no events encode to nothing: %v %v", msgs, err)
	}
	ev := captured()
	ev.AuthorizationID = ""
	if _, err := kafka.NewEncoder().Encode(ev); err == nil {
		t.Fatal("an event without a subject cannot be keyed")
	}
}

func TestEncoderPrefixIsolatesTopicsAndZeroEncoderMintsUUIDs(t *testing.T) {
	msgs, err := (&kafka.Encoder{Prefix: "t1."}).Encode(authorized())
	if err != nil || msgs[0].Topic != "t1."+kafka.TopicEvents || len(msgs[0].EventID) != 36 {
		t.Fatalf("Encode: %v %+v", err, msgs)
	}
}

func TestHeadersConvertToKafkaHeaders(t *testing.T) {
	got := kafka.Headers([]outbox.Header{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}})
	if len(got) != 2 || got[0].Key != "a" || string(got[0].Value) != "1" || got[1].Key != "b" || string(got[1].Value) != "2" {
		t.Fatalf("headers = %+v", got)
	}
}
