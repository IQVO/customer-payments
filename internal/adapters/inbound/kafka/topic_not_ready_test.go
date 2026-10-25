package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

// stubDLQWriter scripts the errors WriteMessages returns, in order.
type stubDLQWriter struct {
	errs  []error
	calls int
}

func (w *stubDLQWriter) WriteMessages(_ context.Context, _ ...kafkago.Message) error {
	w.calls++
	if len(w.errs) == 0 {
		return nil
	}
	err := w.errs[0]
	w.errs = w.errs[1:]
	return err
}

func (*stubDLQWriter) Close() error { return nil }

// TestIsTopicNotReady pins which write errors count as "the auto-created DLQ
// topic has no leader yet" (the only ones writeDLQ retries): the two kafka-go
// sentinels, alone or inside a WriteErrors batch whose every element is
// transient. Anything else — another kafka error, a plain error, a batch with
// one real failure, or an empty batch — is not transient.
func TestIsTopicNotReady(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unknown topic or partition", kafkago.UnknownTopicOrPartition, true},
		{"leader not available", kafkago.LeaderNotAvailable, true},
		{"batch of only not-ready errors", kafkago.WriteErrors{kafkago.UnknownTopicOrPartition, kafkago.LeaderNotAvailable}, true},
		{"batch with a nil entry", kafkago.WriteErrors{nil, kafkago.LeaderNotAvailable}, true},
		{"another kafka error", kafkago.MessageSizeTooLarge, false},
		{"batch with one real failure", kafkago.WriteErrors{kafkago.UnknownTopicOrPartition, kafkago.MessageSizeTooLarge}, false},
		{"plain error", errors.New("dns is gone"), false},
		{"empty batch", kafkago.WriteErrors{nil}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTopicNotReady(tc.err); got != tc.want {
				t.Fatalf("isTopicNotReady(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestWriteDLQ_RetriesWhileTheTopicIsNotReady proves the bounded retry: a
// not-ready error is retried (the DLQ topic is being auto-created) and the
// write succeeds once the leader exists, in the very next attempt.
func TestWriteDLQ_RetriesWhileTheTopicIsNotReady(t *testing.T) {
	w := &stubDLQWriter{errs: []error{kafkago.UnknownTopicOrPartition, kafkago.LeaderNotAvailable}}
	if err := writeDLQ(context.Background(), w, kafkago.Message{Topic: "t.dlq"}); err != nil {
		t.Fatalf("writeDLQ = %v, want nil once the topic is ready", err)
	}
	if w.calls != 3 {
		t.Fatalf("WriteMessages called %d times, want 3 (fail, fail, succeed)", w.calls)
	}
}

// A WriteErrors batch whose every element is not-ready is retried as a whole.
func TestWriteDLQ_RetriesANotReadyBatch(t *testing.T) {
	w := &stubDLQWriter{errs: []error{kafkago.WriteErrors{kafkago.LeaderNotAvailable}}}
	if err := writeDLQ(context.Background(), w, kafkago.Message{}); err != nil {
		t.Fatalf("writeDLQ = %v, want nil", err)
	}
}

// Any other error is returned as-is on the first attempt: retrying cannot help.
func TestWriteDLQ_ReturnsOtherErrorsImmediately(t *testing.T) {
	injected := errors.New("sasl handshake failed")
	w := &stubDLQWriter{errs: []error{injected}}
	err := writeDLQ(context.Background(), w, kafkago.Message{})
	if !errors.Is(err, injected) {
		t.Fatalf("writeDLQ = %v, want the injected error untouched", err)
	}
	if w.calls != 1 {
		t.Fatalf("WriteMessages called %d times, want exactly 1", w.calls)
	}
}

// A context cancelled while backing off stops the retry loop and reports both
// the last write error and the cancellation, so the offset stays uncommitted.
func TestWriteDLQ_CancelledWhileBackingOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &stubDLQWriter{errs: []error{kafkago.UnknownTopicOrPartition, kafkago.UnknownTopicOrPartition}}
	err := writeDLQ(ctx, w, kafkago.Message{})
	if !errors.Is(err, kafkago.UnknownTopicOrPartition) || !errors.Is(err, context.Canceled) {
		t.Fatalf("writeDLQ = %v, want the write error joined with context.Canceled", err)
	}
	if w.calls != 1 {
		t.Fatalf("WriteMessages called %d times, want 1 (cancelled before the retry)", w.calls)
	}
}
