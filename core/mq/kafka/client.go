package kafka

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/surge-go/aurora/core/mq"
)

var errInvalidNativeRecord = errors.New("mq kafka transport record is invalid")

type handlerContextKey struct{}

type partitionKey struct {
	topic     string
	partition int32
}

type partitionWorker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	records  chan *recordTask
	commitMu sync.Mutex
}

type recordTask struct {
	record     *Record
	done       chan struct{}
	workerDone <-chan struct{}
}

// Client implements mq.Publisher and mq.Consumer using Kafka consumer groups.
type Client struct {
	cfg       Config
	transport Transport

	mu            sync.Mutex
	closed        bool
	consuming     bool
	consumeCancel context.CancelFunc
	active        int
	idle          chan struct{}
	closeOnce     sync.Once

	assignmentMu sync.RWMutex
	workers      map[partitionKey]*partitionWorker
}

var (
	_ mq.Publisher = (*Client)(nil)
	_ mq.Consumer  = (*Client)(nil)
)

// NewClient validates local configuration and creates a lazy Kafka client.
// The adapter owns and closes the resulting transport.
func NewClient(cfg *Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	normalized := cfg.normalized()
	tlsConfig, err := buildTLSConfig(normalized.TLS)
	if err != nil {
		return nil, err
	}
	dialer := normalized.Dialer
	if dialer == nil {
		dialer = defaultDialer
	}
	var client *Client
	transport, err := dialer(normalized, tlsConfig, func(topic string, partitions []int32) {
		if client != nil {
			client.revoke(topic, partitions)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("mq kafka create client: %w", err)
	}
	if transport == nil {
		return nil, errors.New("mq kafka dialer returned nil transport")
	}
	client = &Client{cfg: normalized, transport: transport, idle: closedChan(), workers: make(map[partitionKey]*partitionWorker)}
	return client, nil
}

func (c *Client) Publish(ctx context.Context, destination string, message mq.Message) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("%w: destination is required", mq.ErrInvalidArgument)
	}
	route, ok := c.cfg.Routes[destination]
	if !ok {
		return fmt.Errorf("%w: publish route %q is not configured", mq.ErrInvalidArgument, destination)
	}
	if err := c.beginOperation(); err != nil {
		return err
	}
	defer c.finishOperation()
	record, err := encodeRecord(route.Topic, message)
	if err != nil {
		return fmt.Errorf("mq kafka encode message: %w", err)
	}
	if c.cfg.MaxPayload > 0 && recordSize(record) > c.cfg.MaxPayload {
		return fmt.Errorf("%w: record size %d exceeds max %d", mq.ErrInvalidArgument, recordSize(record), c.cfg.MaxPayload)
	}
	waitCtx := ctx
	var cancel context.CancelFunc
	if c.cfg.Producer.Timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, c.cfg.Producer.Timeout)
		defer cancel()
	}
	if err := c.transport.Produce(waitCtx, record); err != nil {
		return fmt.Errorf("mq kafka confirm publish %q: %w", destination, err)
	}
	return nil
}

func (c *Client) Consume(ctx context.Context, destination string, handler mq.Handler) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("%w: destination is required", mq.ErrInvalidArgument)
	}
	if handler == nil {
		return fmt.Errorf("%w: handler is nil", mq.ErrInvalidArgument)
	}
	route, ok := c.cfg.Routes[destination]
	if !ok {
		return fmt.Errorf("%w: consume route %q is not configured", mq.ErrInvalidArgument, destination)
	}
	runCtx, cancel, err := c.startConsume(ctx)
	if err != nil {
		return err
	}
	defer c.finishConsume()
	c.transport.Subscribe(route.Topic)
	workersDone := &sync.WaitGroup{}
	sem := make(chan struct{}, c.cfg.Consumer.Concurrency)
	errorsCh := make(chan error, 1)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		for {
			records, pollErr := c.transport.Poll(runCtx, 100)
			tasks := make([]*recordTask, 0, len(records))
			for _, record := range records {
				if record == nil || record.Topic != route.Topic {
					continue
				}
				worker := c.workerFor(runCtx, record, workersDone, sem, errorsCh, handler)
				task := &recordTask{record: record, done: make(chan struct{}), workerDone: worker.ctx.Done()}
				select {
				case worker.records <- task:
					tasks = append(tasks, task)
				case <-worker.ctx.Done():
				case <-runCtx.Done():
					return
				}
			}
			if runCtx.Err() != nil {
				return
			}
			if pollErr != nil {
				if errors.Is(pollErr, errRecoverablePoll) {
					if err := waitRetry(runCtx, recoverablePollBackoff); err != nil {
						return
					}
					continue
				}
				for _, task := range tasks {
					select {
					case <-task.done:
					case <-task.workerDone:
					case <-runCtx.Done():
						return
					}
				}
				if runCtx.Err() == nil {
					reportError(errorsCh, fmt.Errorf("poll: %w", pollErr))
					cancel()
				}
				return
			}
		}
	}()
	<-pollDone
	workersDone.Wait()
	c.cancelWorkers()
	select {
	case err := <-errorsCh:
		return fmt.Errorf("mq kafka consume %q: %w", destination, err)
	default:
		return nil
	}
}

func (c *Client) workerFor(parent context.Context, record *Record, wg *sync.WaitGroup, sem chan struct{}, errorsCh chan error, handler mq.Handler) *partitionWorker {
	key := partitionKey{topic: record.Topic, partition: record.Partition}
	c.assignmentMu.Lock()
	defer c.assignmentMu.Unlock()
	if worker := c.workers[key]; worker != nil {
		return worker
	}
	ctx, cancel := context.WithCancel(parent)
	worker := &partitionWorker{ctx: ctx, cancel: cancel, records: make(chan *recordTask, 100)}
	c.workers[key] = worker
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-worker.ctx.Done():
				return
			case task := <-worker.records:
				select {
				case sem <- struct{}{}:
				case <-worker.ctx.Done():
					return
				}
				err := c.processRecord(worker.ctx, key, task.record, handler)
				<-sem
				if err != nil {
					if worker.ctx.Err() != nil {
						close(task.done)
						return
					}
					reportError(errorsCh, err)
					c.mu.Lock()
					cancelConsume := c.consumeCancel
					c.mu.Unlock()
					if cancelConsume != nil {
						cancelConsume()
					}
					close(task.done)
					return
				}
				close(task.done)
			}
		}
	}()
	return worker
}

func (c *Client) processRecord(ctx context.Context, key partitionKey, record *Record, handler mq.Handler) error {
	message, err := decodeRecord(record)
	if err != nil {
		return fmt.Errorf("decode %s[%d] offset %d: %w", key.topic, key.partition, record.Offset, err)
	}
	handlerCtx := context.WithValue(ctx, handlerContextKey{}, true)
	var handlerErr error
	attempts := 1
	if c.cfg.Consumer.Failure.Mode == FailureRetry {
		attempts = c.cfg.Consumer.Retry.MaxAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		handlerErr = handler(handlerCtx, message.Clone())
		if handlerErr == nil || ctx.Err() != nil {
			break
		}
		if attempt < attempts {
			if err := waitRetry(ctx, c.cfg.Consumer.Retry.Backoff); err != nil {
				return err
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if handlerErr != nil {
		switch c.cfg.Consumer.Failure.Mode {
		case FailureDeadLetter:
			if err := c.publishDeadLetter(ctx, message, record); err != nil {
				return fmt.Errorf("dead-letter publish: %w", err)
			}
		case FailureDrop:
		case FailureRetry, FailureStop:
			return fmt.Errorf("handler failed at %s[%d] offset %d: %w", key.topic, key.partition, record.Offset, handlerErr)
		default:
			return errors.New("unsupported handler failure mode")
		}
	}
	c.assignmentMu.RLock()
	worker := c.workers[key]
	c.assignmentMu.RUnlock()
	if worker == nil {
		return context.Canceled
	}
	worker.commitMu.Lock()
	defer worker.commitMu.Unlock()
	c.assignmentMu.RLock()
	assigned := c.workers[key] == worker && worker.ctx == ctx && ctx.Err() == nil
	c.assignmentMu.RUnlock()
	if !assigned {
		return context.Canceled
	}
	if err := c.transport.Commit(ctx, record); err != nil {
		return fmt.Errorf("commit %s[%d] offset %d: %w", key.topic, key.partition, record.Offset+1, err)
	}
	return nil
}

func (c *Client) publishDeadLetter(ctx context.Context, message mq.Message, source *Record) error {
	route := c.cfg.Routes[c.cfg.Consumer.Failure.DeadLetterRoute]
	message.Headers = append(message.Headers,
		mq.Header{Key: "x-aurora-mq-source-topic", Value: []byte(source.Topic)},
		mq.Header{Key: "x-aurora-mq-source-partition", Value: []byte(strconv.FormatInt(int64(source.Partition), 10))},
		mq.Header{Key: "x-aurora-mq-source-offset", Value: []byte(strconv.FormatInt(source.Offset, 10))},
		mq.Header{Key: "x-aurora-mq-failure", Value: []byte("handler_error")},
	)
	record, err := encodeRecord(route.Topic, message)
	if err != nil {
		return err
	}
	if c.cfg.MaxPayload > 0 && recordSize(record) > c.cfg.MaxPayload {
		return fmt.Errorf("%w: dead-letter record size %d exceeds max %d", mq.ErrInvalidArgument, recordSize(record), c.cfg.MaxPayload)
	}
	return c.transport.Produce(ctx, record)
}

func (c *Client) revoke(topic string, partitions []int32) {
	c.assignmentMu.Lock()
	var revoked []*partitionWorker
	for _, partition := range partitions {
		key := partitionKey{topic: topic, partition: partition}
		if worker := c.workers[key]; worker != nil {
			worker.cancel()
			delete(c.workers, key)
			revoked = append(revoked, worker)
		}
	}
	c.assignmentMu.Unlock()
	for _, worker := range revoked {
		worker.commitMu.Lock()
		worker.commitMu.Unlock()
	}
}

func (c *Client) cancelWorkers() {
	c.assignmentMu.Lock()
	defer c.assignmentMu.Unlock()
	for key, worker := range c.workers {
		worker.cancel()
		delete(c.workers, key)
	}
}

func (c *Client) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	c.mu.Lock()
	c.closed = true
	cancel := c.consumeCancel
	idle := c.idle
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if isHandlerContext(ctx) {
		go c.closeAfterIdle(idle)
		return nil
	}
	select {
	case <-idle:
		c.closeTransport()
		return nil
	case <-ctx.Done():
		go c.closeAfterIdle(idle)
		return ctx.Err()
	}
}

func (c *Client) closeAfterIdle(idle <-chan struct{}) { <-idle; c.closeTransport() }
func (c *Client) closeTransport()                     { c.closeOnce.Do(c.transport.Close) }

func (c *Client) beginOperation() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return mq.ErrClosed
	}
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
	return nil
}

func (c *Client) finishOperation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if c.active == 0 {
		close(c.idle)
	}
}

func (c *Client) startConsume(ctx context.Context) (context.Context, context.CancelFunc, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, mq.ErrClosed
	}
	if c.consuming {
		return nil, nil, mq.ErrConsumerAlreadyRunning
	}
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
	c.consuming = true
	runCtx, cancel := context.WithCancel(ctx)
	c.consumeCancel = cancel
	return runCtx, cancel, nil
}

func (c *Client) finishConsume() {
	c.mu.Lock()
	c.consuming = false
	c.consumeCancel = nil
	c.active--
	if c.active == 0 {
		close(c.idle)
	}
	c.mu.Unlock()
}

func waitRetry(ctx context.Context, backoff time.Duration) error {
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func reportError(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}
func closedChan() chan struct{} { ch := make(chan struct{}); close(ch); return ch }
func isHandlerContext(ctx context.Context) bool {
	value, _ := ctx.Value(handlerContextKey{}).(bool)
	return value
}
