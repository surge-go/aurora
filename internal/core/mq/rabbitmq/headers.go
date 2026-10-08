package rabbitmq

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/surge-go/aurora/internal/core/mq"
)

const (
	headerEnvelopeKey = "x-aurora-mq-headers-v1"
	headerKeyKey      = "x-aurora-mq-key-v1"
)

type headerEnvelope struct {
	Headers []mq.Header `json:"headers,omitempty"`
}

func encodeHeaders(message mq.Message) (amqp.Table, error) {
	result := make(amqp.Table, 2)
	if len(message.Headers) > 0 {
		payload, err := json.Marshal(headerEnvelope{Headers: message.Headers})
		if err != nil {
			return nil, fmt.Errorf("encode header envelope: %w", err)
		}
		result[headerEnvelopeKey] = payload
		for _, header := range message.Headers {
			if header.Key == headerEnvelopeKey || header.Key == headerKeyKey {
				return nil, fmt.Errorf("%w: header key %q is reserved", mq.ErrInvalidArgument, header.Key)
			}
			result[header.Key] = append([]byte(nil), header.Value...)
		}
	}
	if message.Key != nil {
		result[headerKeyKey] = base64.StdEncoding.EncodeToString(message.Key)
	}
	return result, nil
}

func decodeHeaders(headers amqp.Table) ([]mq.Header, []byte, error) {
	var result []mq.Header
	var key []byte
	if raw, ok := headers[headerEnvelopeKey]; ok {
		payload, err := tableBytes(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("decode header envelope: %w", err)
		}
		var envelope headerEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, nil, fmt.Errorf("decode header envelope: %w", err)
		}
		result = make([]mq.Header, len(envelope.Headers))
		for i, header := range envelope.Headers {
			result[i] = mq.Header{Key: header.Key, Value: append([]byte(nil), header.Value...)}
		}
	}
	if raw, ok := headers[headerKeyKey]; ok {
		value, err := tableBytes(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("decode message key: %w", err)
		}
		if decoded, err := base64.StdEncoding.DecodeString(string(value)); err == nil {
			key = decoded
		} else {
			return nil, nil, fmt.Errorf("decode message key: %w", err)
		}
	}
	if _, hasEnvelope := headers[headerEnvelopeKey]; !hasEnvelope {
		keys := make([]string, 0, len(headers))
		for key := range headers {
			if key != headerKeyKey {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, err := tableBytes(headers[key])
			if err != nil {
				continue
			}
			result = append(result, mq.Header{Key: key, Value: value})
		}
	}
	return result, key, nil
}

func tableBytes(value any) ([]byte, error) {
	switch value := value.(type) {
	case []byte:
		return append([]byte(nil), value...), nil
	case string:
		return []byte(value), nil
	default:
		return nil, errors.New("header value must be bytes or string")
	}
}
