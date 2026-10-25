package kafka

import (
	"context"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

type fakeConn struct {
	list []kafkago.Broker
	err  error
}

func (f fakeConn) Brokers() ([]kafkago.Broker, error) { return f.list, f.err }
func (f fakeConn) Close() error                       { return nil }

func checkWith(brokers []string, dial func(string) (brokerLister, error)) error {
	c := NewBrokerCheck(brokers)
	c.dial = func(_ context.Context, addr string) (brokerLister, error) { return dial(addr) }
	return c.Check(context.Background())
}

func TestBrokerCheck(t *testing.T) {
	ok := fakeConn{list: []kafkago.Broker{{ID: 1}}}
	down := errors.New("connection refused")
	for _, c := range []struct {
		name    string
		brokers []string
		dial    func(string) (brokerLister, error)
		wantErr bool
	}{
		{"none configured", nil, nil, true},
		{"one answers", []string{"a:9092"}, func(string) (brokerLister, error) { return ok, nil }, false},
		{"second answers after the first is down", []string{"a:9092", "b:9092"}, func(addr string) (brokerLister, error) {
			if addr == "a:9092" {
				return nil, down
			}
			return ok, nil
		}, false},
		{"all down", []string{"a:9092", "b:9092"}, func(string) (brokerLister, error) { return nil, down }, true},
		{"metadata error", []string{"a:9092"}, func(string) (brokerLister, error) { return fakeConn{err: down}, nil }, true},
		{"empty cluster", []string{"a:9092"}, func(string) (brokerLister, error) { return fakeConn{}, nil }, true},
		{"address without a port", []string{"nohost"}, func(string) (brokerLister, error) { return ok, nil }, true},
	} {
		if err := checkWith(c.brokers, c.dial); (err != nil) != c.wantErr {
			t.Fatalf("%s: err = %v", c.name, err)
		}
	}
}

func TestBrokerCheckAgainstADeadAddressFails(t *testing.T) {
	if err := NewBrokerCheck([]string{"127.0.0.1:1"}).Check(context.Background()); err == nil {
		t.Fatal("nothing listens on port 1")
	}
}
