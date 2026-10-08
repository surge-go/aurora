package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/internal/core/mq"
)

func TestPublishAndConsumeRedisStreams(t *testing.T) {
	addr := os.Getenv("MQ_REDIS_ADDR")
	if addr == "" {
		t.Skip("set MQ_REDIS_ADDR to run Redis Streams integration test")
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stream := "mq-integration-" + suffix
	group := "orders-group-" + suffix
	consumer := "consumer-" + suffix

	client, err := NewClient(&Config{
		Client:    rdb,
		Group:     group,
		Consumer:  consumer,
		StartID:   "0",
		Block:     100 * time.Millisecond,
		ReadCount: 1,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())

	want := mq.Message{ID: "order-1", Body: []byte(`{"status":"created"}`)}
	if err := client.Publish(ctx, stream, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	received := make(chan mq.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- client.Consume(consumeCtx, stream, func(_ context.Context, message mq.Message) error {
			received <- message
			return nil
		})
	}()

	select {
	case got := <-received:
		if got.ID != want.ID || string(got.Body) != string(want.Body) {
			t.Fatalf("received = %+v, want %+v", got, want)
		}
		stop()
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out stopping consumer")
	}
}

func TestClaimCursorReachesLaterPendingEntries(t *testing.T) {
	addr := os.Getenv("MQ_REDIS_ADDR")
	if addr == "" {
		t.Skip("set MQ_REDIS_ADDR to run Redis Streams integration test")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stream := "mq-claim-" + suffix
	group := "claim-group-" + suffix
	consumer := "claim-consumer-" + suffix
	for i := 0; i < 3; i++ {
		if err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: map[string]interface{}{payloadField: []byte(`{"id":"x"}`)}}).Err(); err != nil {
			t.Fatalf("XAdd: %v", err)
		}
	}
	if err := rdb.XGroupCreateMkStream(ctx, stream, group, "0-0").Err(); err != nil {
		t.Fatalf("XGroupCreate: %v", err)
	}
	entries, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{Group: group, Consumer: "old", Streams: []string{stream, ">"}, Count: 3}).Result()
	if err != nil || len(entries) != 1 || len(entries[0].Messages) != 3 {
		t.Fatalf("seed pending entries = %v, %v", entries, err)
	}
	if err := rdb.XClaim(ctx, &redis.XClaimArgs{Stream: stream, Group: group, Consumer: consumer, MinIdle: 0, Messages: []string{entries[0].Messages[0].ID}}).Err(); err != nil {
		t.Fatalf("seed first claim: %v", err)
	}

	client, err := NewClient(&Config{Client: rdb, Group: group, Consumer: consumer, ReadCount: 1, Block: 50 * time.Millisecond, ClaimIdle: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close(context.Background())
	seen := make(chan string, 2)
	consumeCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = client.Consume(consumeCtx, stream, func(_ context.Context, message mq.Message) error {
			seen <- message.ID
			return nil
		})
	}()
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal("timed out waiting for claimed messages")
	}
}
