package eventbus

import (
	"context"
	"errors"
)

var ErrClosed = errors.New("event bus is closed")

type Message struct {
	Topic   string
	Key     []byte
	Body    []byte
	Headers map[string][]byte
}

type Subscription struct {
	Topics []string
	Group  string
}

type Handler func(context.Context, Message) error

type Bus interface {
	Publish(context.Context, Message) error
	Consume(context.Context, Subscription, Handler) error
}

type Connector interface {
	Bus
	Ping(context.Context) error
	Close() error
}

type Factory interface {
	Open(context.Context) (Connector, error)
}
