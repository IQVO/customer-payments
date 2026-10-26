package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"

	kafkago "github.com/segmentio/kafka-go"
)

// BrokerCheck is the /readyz Kafka dependency: some broker answers a topic-less
// metadata request (never topics: the broker auto-creates them on first write).
type BrokerCheck struct {
	brokers []string
	dial    func(ctx context.Context, addr string) (brokerLister, error)
}

type brokerLister interface {
	Brokers() ([]kafkago.Broker, error)
	Close() error
}

// NewBrokerCheck builds a check over brokers.
func NewBrokerCheck(brokers []string) *BrokerCheck {
	return &BrokerCheck{brokers: brokers, dial: func(ctx context.Context, addr string) (brokerLister, error) {
		conn, err := (&kafkago.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}}
}

// Check succeeds when any broker lists the cluster.
func (c *BrokerCheck) Check(ctx context.Context) error {
	if len(c.brokers) == 0 {
		return errors.New("no kafka brokers configured")
	}
	var errs []error
	for _, addr := range c.brokers {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			errs = append(errs, fmt.Errorf("broker %q: %w", addr, err))
			continue
		}
		conn, err := c.dial(ctx, addr)
		if err != nil {
			errs = append(errs, fmt.Errorf("dial %s: %w", addr, err))
			continue
		}
		list, err := conn.Brokers()
		_ = conn.Close()
		if err == nil && len(list) > 0 {
			return nil
		}
		if err == nil {
			err = errors.New("the broker lists no brokers")
		}
		errs = append(errs, fmt.Errorf("metadata from %s: %w", addr, err))
	}
	return errors.Join(errs...)
}
