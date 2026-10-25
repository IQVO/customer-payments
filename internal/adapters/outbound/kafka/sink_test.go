package kafka_test

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/IQVO/customer-payments/internal/adapters/outbound/kafka"
	"github.com/IQVO/customer-payments/internal/application/outbox"
)

type fakeWriter struct {
	got []kafkago.Message
	err error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.got = append(w.got, msgs...)
	return w.err
}

func TestSinkWritesEachMessageToItsOwnTopic(t *testing.T) {
	w := &fakeWriter{}
	err := kafka.Sink{W: w}.Send(context.Background(),
		outbox.Message{Topic: "t1", Key: []byte("k1"), Value: []byte("v1"), Headers: []outbox.Header{{Key: "content-type", Value: "x"}}},
		outbox.Message{Topic: "t2", Key: []byte("k2"), Value: []byte("v2")})
	if err != nil || len(w.got) != 2 {
		t.Fatalf("err=%v got=%+v", err, w.got)
	}
	if w.got[0].Topic != "t1" || string(w.got[0].Key) != "k1" || string(w.got[0].Value) != "v1" || w.got[0].Headers[0].Key != "content-type" || w.got[1].Topic != "t2" {
		t.Fatalf("messages = %+v", w.got)
	}
}

func TestSinkReportsAWriteFailure(t *testing.T) {
	boom := errors.New("broker down")
	if err := (kafka.Sink{W: &fakeWriter{err: boom}}).Send(context.Background(), outbox.Message{Topic: "t"}); !errors.Is(err, boom) {
		t.Fatalf("want the write error, got %v", err)
	}
}
