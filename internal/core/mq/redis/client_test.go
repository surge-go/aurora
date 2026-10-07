package redis

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/internal/core/mq"
)

func TestConfigValidate(t *testing.T) {
	var typedNilClient *redis.Client
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "missing client", cfg: Config{Group: "g", Consumer: "c"}, want: "client is required"},
		{name: "typed nil client", cfg: Config{Client: typedNilClient, Group: "g", Consumer: "c"}, want: "client is required"},
		{name: "missing group", cfg: Config{Client: &redis.Client{}, Consumer: "c"}, want: "group is required"},
		{name: "negative block", cfg: Config{Client: &redis.Client{}, Group: "g", Consumer: "c", Block: -time.Second}, want: "block must be non-negative"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil || !contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestConfigNormalizedDefaults(t *testing.T) {
	cfg := (Config{StartID: "", ReadCount: 0, Block: 0}).normalized()
	if cfg.StartID != "0-0" || cfg.ReadCount != 10 || cfg.Block != 5*time.Second || cfg.ClaimIdle != 30*time.Second {
		t.Fatalf("normalized config = %+v", cfg)
	}

	disabled := (Config{DisableClaim: true}).normalized()
	if disabled.ClaimIdle != 0 {
		t.Fatalf("disabled reclaim interval = %v, want 0", disabled.ClaimIdle)
	}
}

func TestDecodeMessage(t *testing.T) {
	message := mq.Message{
		ID:        "business-id",
		Key:       []byte("partition-key"),
		Body:      []byte(`{"event":"created"}`),
		Headers:   []mq.Header{{Key: "trace", Value: []byte("trace-id")}},
		Timestamp: time.Unix(1700000000, 123),
	}
	payload, err := encodeMessage(message)
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	got, err := decodeMessage(redis.XMessage{ID: "0-1", Values: map[string]interface{}{payloadField: payload}})
	if err != nil {
		t.Fatalf("decodeMessage() error = %v", err)
	}
	if got.ID != message.ID || string(got.Key) != string(message.Key) || string(got.Body) != string(message.Body) || !got.Timestamp.Equal(message.Timestamp) {
		t.Fatalf("decoded message = %+v", got)
	}
	got.Body[0] = 'X'
	if string(message.Body) != `{"event":"created"}` {
		t.Fatalf("decoded body aliases source: %q", message.Body)
	}
}

func TestDecodeMessageFallsBackToStreamID(t *testing.T) {
	payload, err := encodeMessage(mq.Message{Body: []byte("body")})
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	got, err := decodeMessage(redis.XMessage{ID: "12-3", Values: map[string]interface{}{payloadField: payload}})
	if err != nil {
		t.Fatalf("decodeMessage() error = %v", err)
	}
	if got.ID != "12-3" {
		t.Fatalf("decoded ID = %q, want stream ID", got.ID)
	}
	if !got.Timestamp.IsZero() {
		t.Fatalf("decoded zero timestamp = %v", got.Timestamp)
	}
}

func contains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
