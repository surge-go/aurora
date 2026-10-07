package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/internal/core/mq"
)

const payloadField = "__mq_payload"

type handlerContextKey struct{}

type wireMessage struct {
	ID        string      `json:"id,omitempty"`
	Key       []byte      `json:"key,omitempty"`
	Body      []byte      `json:"body,omitempty"`
	Headers   []mq.Header `json:"headers,omitempty"`
	Timestamp *int64      `json:"timestamp_unix_nano,omitempty"`
}

// Client implements mq.Publisher and mq.Consumer with Redis Streams.
type Client struct {
	client redis.UniversalClient
	cfg    Config

	mu            sync.Mutex
	closed        bool
	consumeCancel context.CancelFunc
	consumeDone   chan struct{}
	active        int
	idle          chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

var (
	_ mq.Publisher = (*Client)(nil)
	_ mq.Consumer  = (*Client)(nil)
)

// NewClient creates a Redis Streams adapter around an existing Redis client.
// It does not connect to Redis or create a consumer group until Consume is
// called. The injected client is borrowed unless Config.OwnClient is true.
func NewClient(cfg *Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	normalized := cfg.normalized()
	return &Client{client: normalized.Client, cfg: normalized, idle: closedChan()}, nil
}

func (c *Client) Publish(ctx context.Context, destination string, message mq.Message) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	if err := invalidDestination(destination); err != nil {
		return err
	}
	if err := c.beginOperation(); err != nil {
		return err
	}
	defer c.endOperation()
	payload, err := encodeMessage(message)
	if err != nil {
		return fmt.Errorf("mq redis encode message: %w", err)
	}
	if c.cfg.MaxPayload > 0 && len(payload) > c.cfg.MaxPayload {
		return fmt.Errorf("%w: payload size %d exceeds max %d", mq.ErrInvalidArgument, len(payload), c.cfg.MaxPayload)
	}
	if err := c.client.XAdd(ctx, &redis.XAddArgs{
		Stream: destination,
		ID:     "*",
		Values: map[string]interface{}{payloadField: payload},
	}).Err(); err != nil {
		return fmt.Errorf("mq redis publish stream %q: %w", destination, err)
	}
	return nil
}

func encodeMessage(message mq.Message) ([]byte, error) {
	var timestamp *int64
	if !message.Timestamp.IsZero() {
		value := message.Timestamp.UnixNano()
		timestamp = &value
	}
	return json.Marshal(wireMessage{
		ID:        message.ID,
		Key:       message.Key,
		Body:      message.Body,
		Headers:   message.Headers,
		Timestamp: timestamp,
	})
}

// Consume reads new messages from a Redis Streams consumer group. Successful
// handlers are acknowledged. Handler errors leave messages pending; when
// ClaimIdle is configured, subsequent reads can reclaim idle pending entries.
func (c *Client) Consume(ctx context.Context, destination string, handler mq.Handler) error {
	if err := invalidDestination(destination); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("%w: handler is nil", mq.ErrInvalidArgument)
	}
	consumeCtx, done, err := c.startConsume(ctx)
	if err != nil {
		return err
	}
	defer c.finishConsume(done)

	if err := c.ensureGroup(consumeCtx, destination); err != nil {
		if consumeCtx.Err() != nil {
			return nil
		}
		return fmt.Errorf("mq redis create consumer group: %w", err)
	}
	claimCursor := "0-0"
	for {
		if err := consumeCtx.Err(); err != nil {
			return nil
		}
		if c.cfg.ClaimIdle > 0 && !c.cfg.DisableClaim {
			claimed, nextCursor, err := c.client.XAutoClaim(consumeCtx, &redis.XAutoClaimArgs{
				Stream:   destination,
				Group:    c.cfg.Group,
				Consumer: c.cfg.Consumer,
				MinIdle:  c.cfg.ClaimIdle,
				Start:    claimCursor,
				Count:    c.cfg.ReadCount,
			}).Result()
			if err != nil {
				if consumeCtx.Err() != nil {
					return nil
				}
				return fmt.Errorf("mq redis reclaim stream %q: %w", destination, err)
			}
			if nextCursor == "" {
				nextCursor = "0-0"
			}
			claimCursor = nextCursor
			if err := c.processMessages(consumeCtx, destination, handler, claimed); err != nil {
				return err
			}
		}
		streams, err := c.client.XReadGroup(consumeCtx, &redis.XReadGroupArgs{
			Group:    c.cfg.Group,
			Consumer: c.cfg.Consumer,
			Streams:  []string{destination, ">"},
			Count:    c.cfg.ReadCount,
			Block:    c.cfg.Block,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if consumeCtx.Err() != nil {
				return nil
			}
			return fmt.Errorf("mq redis read stream %q: %w", destination, err)
		}
		for _, stream := range streams {
			if err := c.processMessages(consumeCtx, destination, handler, stream.Messages); err != nil {
				return err
			}
		}
	}
}

func (c *Client) processMessages(ctx context.Context, destination string, handler mq.Handler, entries []redis.XMessage) error {
	for _, entry := range entries {
		message, err := decodeMessage(entry)
		if err != nil {
			return fmt.Errorf("mq redis decode stream %q entry %q: %w", destination, entry.ID, err)
		}
		handlerCtx := context.WithValue(ctx, handlerContextKey{}, true)
		if err := handler(handlerCtx, message); err != nil {
			continue
		}
		if err := c.client.XAck(ctx, destination, c.cfg.Group, entry.ID).Err(); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("mq redis acknowledge stream %q entry %q: %w", destination, entry.ID, err)
		}
	}
	return nil
}

// Close cancels an active consumer and closes the injected Redis client only
// when OwnClient is enabled. A borrowed Redis client remains application-owned.
func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
	}
	cancel := c.consumeCancel
	idle := c.idle
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if isHandlerContext(ctx) {
		if c.cfg.OwnClient {
			go c.closeAfterIdle(idle)
		}
		return nil
	}
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !c.cfg.OwnClient {
		return nil
	}
	c.closeOnce.Do(func() { c.closeErr = c.client.Close() })
	return c.closeErr
}

func (c *Client) closeAfterIdle(idle <-chan struct{}) {
	if idle != nil {
		<-idle
	}
	c.closeOnce.Do(func() { c.closeErr = c.client.Close() })
}

func isHandlerContext(ctx context.Context) bool {
	value, ok := ctx.Value(handlerContextKey{}).(bool)
	return ok && value
}

func (c *Client) beginOperation() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return mq.ErrClosed
	}
	c.startOperationLocked()
	return nil
}

func (c *Client) endOperation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if c.active == 0 {
		close(c.idle)
	}
}

func (c *Client) startConsume(ctx context.Context) (context.Context, chan struct{}, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, mq.ErrClosed
	}
	if c.consumeCancel != nil {
		return nil, nil, mq.ErrConsumerAlreadyRunning
	}
	consumeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.consumeCancel = cancel
	c.consumeDone = done
	c.startOperationLocked()
	return consumeCtx, done, nil
}

func (c *Client) finishConsume(done chan struct{}) {
	c.mu.Lock()
	c.consumeCancel = nil
	c.consumeDone = nil
	c.mu.Unlock()
	c.endOperation()
	close(done)
}

func (c *Client) startOperationLocked() {
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (c *Client) ensureGroup(ctx context.Context, stream string) error {
	err := c.client.XGroupCreateMkStream(ctx, stream, c.cfg.Group, c.cfg.StartID).Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func decodeMessage(entry redis.XMessage) (mq.Message, error) {
	raw, ok := entry.Values[payloadField]
	if !ok {
		return mq.Message{}, errors.New("missing payload field")
	}
	var payload []byte
	switch value := raw.(type) {
	case string:
		payload = []byte(value)
	case []byte:
		payload = append([]byte(nil), value...)
	default:
		payload = []byte(fmt.Sprint(value))
	}
	var wire wireMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		return mq.Message{}, fmt.Errorf("decode payload: %w", err)
	}
	message := mq.Message{
		ID:      wire.ID,
		Key:     append([]byte(nil), wire.Key...),
		Body:    append([]byte(nil), wire.Body...),
		Headers: append([]mq.Header(nil), wire.Headers...),
	}
	if wire.Timestamp != nil {
		message.Timestamp = time.Unix(0, *wire.Timestamp)
	}
	if message.ID == "" {
		message.ID = entry.ID
	}
	for i := range message.Headers {
		message.Headers[i].Value = append([]byte(nil), message.Headers[i].Value...)
	}
	return message, nil
}
