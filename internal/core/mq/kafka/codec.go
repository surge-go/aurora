package kafka

import (
	"fmt"
	"unicode/utf8"

	"github.com/surge-go/aurora/internal/core/mq"
)

func encodeRecord(topic string, message mq.Message) (*Record, error) {
	message = message.Clone()
	record := &Record{
		Topic:     topic,
		Key:       message.Key,
		Value:     message.Body,
		Timestamp: message.Timestamp,
		Headers:   make([]RecordHeader, 0, len(message.Headers)+1),
	}
	for _, header := range message.Headers {
		if header.Key == "" || !utf8.ValidString(header.Key) {
			return nil, fmt.Errorf("%w: header key must be non-empty valid UTF-8", mq.ErrInvalidArgument)
		}
		if header.Key == messageIDHeader {
			return nil, fmt.Errorf("%w: header key %q is reserved", mq.ErrInvalidArgument, messageIDHeader)
		}
		record.Headers = append(record.Headers, RecordHeader{Key: header.Key, Value: header.Value})
	}
	if message.ID != "" {
		record.Headers = append(record.Headers, RecordHeader{Key: messageIDHeader, Value: []byte(message.ID)})
	}
	return record, nil
}

func decodeRecord(record *Record) (mq.Message, error) {
	message := mq.Message{
		Key:       cloneBytes(record.Key),
		Body:      cloneBytes(record.Value),
		Timestamp: record.Timestamp,
		Headers:   make([]mq.Header, 0, len(record.Headers)),
	}
	foundID := false
	for _, header := range record.Headers {
		if header.Key == "" || !utf8.ValidString(header.Key) {
			return mq.Message{}, fmt.Errorf("%w: header key must be non-empty valid UTF-8", mq.ErrInvalidArgument)
		}
		if header.Key == messageIDHeader {
			if foundID {
				return mq.Message{}, fmt.Errorf("%w: duplicate reserved message id header", mq.ErrInvalidArgument)
			}
			message.ID = string(header.Value)
			foundID = true
			continue
		}
		message.Headers = append(message.Headers, mq.Header{Key: header.Key, Value: cloneBytes(header.Value)})
	}
	return message, nil
}

func recordSize(record *Record) int {
	size := len(record.Topic) + len(record.Key) + len(record.Value) + 32
	for _, header := range record.Headers {
		size += len(header.Key) + len(header.Value) + 16
	}
	return size
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	return append([]byte(nil), input...)
}
