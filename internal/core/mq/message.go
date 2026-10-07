// Package mq defines broker-independent message queue contracts.
package mq

import (
	"context"
	"time"
)

// Header is a message metadata entry. Multiple entries may share the same key.
type Header struct {
	Key   string
	Value []byte
}

// Message is an opaque payload and its broker-independent metadata.
//
// ID is supplied by the caller and should be stable when retries must be
// correlated. Key is a routing or partition affinity hint; it does not imply
// an ordering guarantee. Body is serialized by the caller.
type Message struct {
	ID        string
	Key       []byte
	Body      []byte
	Headers   []Header
	Timestamp time.Time
}

// Clone returns a deep copy of the message's mutable byte slices.
func (m Message) Clone() Message {
	clone := m
	clone.Key = cloneBytes(m.Key)
	clone.Body = cloneBytes(m.Body)
	if m.Headers != nil {
		clone.Headers = make([]Header, len(m.Headers))
		for i, header := range m.Headers {
			clone.Headers[i] = Header{
				Key:   header.Key,
				Value: cloneBytes(header.Value),
			}
		}
	}
	return clone
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte{}, value...)
}

// Handler processes one message. A nil error indicates successful processing;
// the Consumer implementation defines how a non-nil error affects delivery.
// The Consumer must pass an independently owned deep copy and must not mutate
// its byte slices until the handler returns. Call Clone to retain the message
// beyond the handler call.
type Handler func(context.Context, Message) error
