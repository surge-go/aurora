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
