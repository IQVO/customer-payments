package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/IQVO/customer-payments/internal/adapters/kafka/cloudevents"
)

// errBadPayload marks a CloudEvent whose `data` cannot be decoded or applied:
// a deterministic problem, never retried.
var errBadPayload = errors.New("malformed event payload")

// errPoison marks a message the consumer can never apply: the run loop
// dead-letters it at once, without retrying, and commits past it.
var errPoison = errors.New("poison message")

// HandlerFunc applies one decoded CloudEvent. nil means done; a non-nil error
// is transient unless it wraps errBadPayload.
type HandlerFunc func(ctx context.Context, e ce.Event) error

// Consumer reads Topic under a stable consumer group and dispatches each
// message on the FULL CloudEvents `type` to its handler. Types it has no
// handler for are ignored.
type Consumer struct {
	Name     string
	Topic    string
	Reader   Reader
	Handlers map[string]HandlerFunc
	Logger   *slog.Logger
	Retry    RetryPolicy

	// DLQ is the dead-letter writer (topic Topic + DLQSuffix): a transient
	// failure is retried domainMaxHandlerAttempts times before the message is
	// published there instead of blocking the partition forever.
	DLQ DeadLetterWriter

	// DeadLetterInvalid dead-letters a CloudEvent of a handled type whose
	// payload can never be applied at once instead of skipping it (ADR 0003).
	// Not-a-CloudEvent and unknown types are still skipped.
	DeadLetterInvalid bool

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// newConsumer builds a Consumer reading topic from brokers under groupID (an
// environment variable at the composition root, never a literal). Constructing
// the reader and the writer does not dial.
func newConsumer(name string, brokers []string, topic, groupID string, handlers map[string]HandlerFunc, logger *slog.Logger) *Consumer {
	return &Consumer{
		Name: name, Topic: topic, Reader: kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Handlers: handlers, Logger: defaultLogger(logger), DLQ: newDomainDLQWriter(brokers),
		DeadLetterInvalid: true,
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil; a transient failure
// retries the SAME message with capped exponential backoff, up to
// domainMaxHandlerAttempts, then dead-letters it.
func (c *Consumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger), name: c.Name, retry: c.Retry, sleep: c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, c.Topic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *Consumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and dispatches it. It
// returns nil for everything deterministic and skippable (not a CloudEvent,
// an unknown type) and a non-nil error for a transient failure (after the unit
// of work rolled back) or a poison message.
func (c *Consumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "consumer", c.Name, "topic", c.Topic, "error", err)
		return nil
	}
	handle, ok := c.Handlers[e.Type()]
	if !ok {
		return nil
	}
	err = handle(ctx, e)
	if !errors.Is(err, errBadPayload) {
		return err
	}
	if c.DeadLetterInvalid && c.DLQ != nil {
		logger.WarnContext(ctx, "dead-lettering an event that can never be applied", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
		return fmt.Errorf("%w: %w", errPoison, err)
	}
	logger.WarnContext(ctx, "skipping an event that can never be applied", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
	return nil
}

// decodeData decodes e's JSON `data` into a T.
func decodeData[T any](e ce.Event) (T, error) {
	var v T
	if err := e.DataAs(&v); err != nil {
		return v, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	return v, nil
}
