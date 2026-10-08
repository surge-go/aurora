package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/surge-go/aurora/internal/core/mq"
)

type handlerContextKey struct{}

var errConsumerDisconnected = errors.New("rabbitmq consumer channel closed")

// Client implements mq.Publisher and mq.Consumer using RabbitMQ AMQP 0.9.1.
type Client struct {
	cfg    Config
	dialer Dialer
	tls    *tls.Config

	mu               sync.Mutex
	connectMu        sync.Mutex
	publishMu        sync.Mutex
	closed           bool
	conn             Connection
	publisher        Channel
	publisherReturns <-chan amqp.Return
	consumer         Channel

	consumeCancel context.CancelFunc
	active        int
	idle          chan struct{}
	closeOnce     sync.Once
	closeErr      error
}

var (
	_ mq.Publisher = (*Client)(nil)
	_ mq.Consumer  = (*Client)(nil)
)

// NewClient validates local configuration but does not connect to RabbitMQ.
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
	return &Client{cfg: normalized, dialer: dialer, tls: tlsConfig, idle: closedChan()}, nil
}

func (c *Client) Publish(ctx context.Context, destination string, message mq.Message) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	if err := invalidDestination(destination); err != nil {
		return err
	}
	route, ok := c.cfg.Routes[destination]
	if !ok || !publishRoute(route) {
		return fmt.Errorf("%w: publish route %q is not configured", mq.ErrInvalidArgument, destination)
	}
	if err := c.beginOperation(); err != nil {
		return err
	}
	defer c.endOperation()

	message = message.Clone()
	headerTable, err := encodeHeaders(message)
	if err != nil {
		return fmt.Errorf("mq rabbitmq encode message: %w", err)
	}
	if c.cfg.MaxPayload > 0 && len(message.Body)+headerSize(headerTable) > c.cfg.MaxPayload {
		return fmt.Errorf("%w: payload size %d exceeds max %d", mq.ErrInvalidArgument, len(message.Body)+headerSize(headerTable), c.cfg.MaxPayload)
	}

	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	channel, returns, err := c.ensurePublisher(ctx, route)
	if err != nil {
		return err
	}
	if err := c.declareRoute(channel, route); err != nil {
		return err
	}
	routingKey := route.RoutingKey
	if routingKey == "" && route.Exchange == "" {
		routingKey = route.Queue
	}
	publishing := amqp.Publishing{
		Headers:      headerTable,
		MessageId:    message.ID,
		Timestamp:    message.Timestamp,
		Body:         append([]byte(nil), message.Body...),
		DeliveryMode: amqp.Persistent,
	}
	confirmation, err := channel.PublishWithDeferredConfirm(route.Exchange, routingKey, c.cfg.Publisher.Mandatory, false, publishing)
	if err != nil {
		c.resetPublisher()
		return fmt.Errorf("mq rabbitmq publish %q: %w", destination, err)
	}
	if confirmation == nil {
		return errors.New("mq rabbitmq publish confirmation is unavailable")
	}
	waitCtx := ctx
	var cancel context.CancelFunc
	if timeout := c.cfg.Publisher.Timeout; timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := waitConfirmation(waitCtx, confirmation, returns); err != nil {
		// A return or confirmation may arrive after the caller's deadline. Close
		// this channel so late notifications cannot be attributed to a later
		// publish on the shared channel.
		c.resetPublisher()
		return fmt.Errorf("mq rabbitmq confirm publish %q: %w", destination, err)
	}
	return nil
}

func (c *Client) Consume(ctx context.Context, destination string, handler mq.Handler) error {
	if err := invalidDestination(destination); err != nil {
		return err
	}
	if handler == nil {
		return fmt.Errorf("%w: handler is nil", mq.ErrInvalidArgument)
	}
	route, ok := c.cfg.Routes[destination]
	if !ok || route.Queue == "" {
		return fmt.Errorf("%w: consume route %q is not configured", mq.ErrInvalidArgument, destination)
	}
	consumeCtx, err := c.startConsume(ctx)
	if err != nil {
		return err
	}
	defer c.finishConsume()

	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = c.consumeOnce(consumeCtx, destination, route, handler)
		if lastErr == nil || consumeCtx.Err() != nil {
			return nil
		}
		if !isRecoverableConsumeError(lastErr) {
			return fmt.Errorf("mq rabbitmq consume %q: %w", destination, lastErr)
		}
		c.resetConnection()
		if attempt >= c.cfg.Consumer.ReconnectAttempts {
			return fmt.Errorf("mq rabbitmq consume %q: %w", destination, lastErr)
		}
		if err := waitBackoff(consumeCtx, c.cfg.Consumer.ReconnectBackoff, attempt); err != nil {
			return nil
		}
	}
}

func (c *Client) consumeOnce(ctx context.Context, destination string, route RouteConfig, handler mq.Handler) error {
	channel, err := c.ensureConsumer(ctx, route)
	if err != nil {
		return err
	}
	if err := channel.Qos(c.cfg.Consumer.Prefetch, 0, false); err != nil {
		return fmt.Errorf("configure consumer qos: %w", err)
	}
	deliveries, err := channel.ConsumeWithContext(ctx, route.Queue, c.cfg.Consumer.ConsumerTag, false, route.Exclusive, false, false, nil)
	if err != nil {
		return fmt.Errorf("start consumer: %w", err)
	}
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	errorsCh := make(chan error, 1)
	sem := make(chan struct{}, c.cfg.Consumer.Concurrency)
	var workers sync.WaitGroup

	for {
		select {
		case <-runCtx.Done():
			workers.Wait()
			if err := firstError(errorsCh); err != nil {
				return err
			}
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				workers.Wait()
				if err := firstError(errorsCh); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				return errConsumerDisconnected
			}
			select {
			case sem <- struct{}{}:
			case <-runCtx.Done():
				workers.Wait()
				if err := firstError(errorsCh); err != nil {
					return err
				}
				return nil
			}
			workers.Add(1)
			go func(delivery amqp.Delivery) {
				defer workers.Done()
				defer func() { <-sem }()
				if err := c.processDelivery(runCtx, delivery, handler); err != nil {
					select {
					case errorsCh <- err:
					default:
					}
					runCancel()
				}
			}(delivery)
		}
	}
}

func (c *Client) processDelivery(ctx context.Context, delivery amqp.Delivery, handler mq.Handler) error {
	message, err := decodeDelivery(delivery)
	if err != nil {
		return fmt.Errorf("decode delivery: %w", err)
	}
	handlerCtx := context.WithValue(ctx, handlerContextKey{}, true)
	if err := handler(handlerCtx, message); err == nil {
		if err := delivery.Ack(false); err != nil {
			return fmt.Errorf("ack delivery: %w", err)
		}
		return nil
	}
	switch c.cfg.Failure.Mode {
	case FailureRequeue:
		if err := delivery.Nack(false, true); err != nil {
			return fmt.Errorf("requeue delivery: %w", err)
		}
	case FailureReject, FailureDeadLetter:
		if err := delivery.Reject(false); err != nil {
			return fmt.Errorf("reject delivery: %w", err)
		}
	default:
		return errors.New("unsupported failure mode")
	}
	return nil
}

func decodeDelivery(delivery amqp.Delivery) (mq.Message, error) {
	headers, key, err := decodeHeaders(delivery.Headers)
	if err != nil {
		return mq.Message{}, err
	}
	id := delivery.MessageId
	if id == "" {
		id = fmt.Sprintf("%s:%s:%d", delivery.Exchange, delivery.RoutingKey, delivery.DeliveryTag)
	}
	return mq.Message{
		ID:        id,
		Key:       key,
		Body:      append([]byte(nil), delivery.Body...),
		Headers:   headers,
		Timestamp: delivery.Timestamp,
	}, nil
}

func (c *Client) ensurePublisher(ctx context.Context, route RouteConfig) (Channel, <-chan amqp.Return, error) {
	conn, err := c.ensureConnection(ctx)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	if c.publisher != nil {
		channel := c.publisher
		returns := c.publisherReturns
		c.mu.Unlock()
		return channel, returns, nil
	}
	c.mu.Unlock()
	channel, err := conn.Channel()
	if err != nil {
		return nil, nil, fmt.Errorf("open publisher channel: %w", err)
	}
	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		return nil, nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	returns := channel.NotifyReturn(make(chan amqp.Return, 16))
	if err := c.declareRoute(channel, route); err != nil {
		_ = channel.Close()
		return nil, nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = channel.Close()
		return nil, nil, mq.ErrClosed
	}
	if c.publisher == nil {
		c.publisher = channel
		c.publisherReturns = returns
		c.mu.Unlock()
		return channel, returns, nil
	}
	existing, existingReturns := c.publisher, c.publisherReturns
	c.mu.Unlock()
	_ = channel.Close()
	return existing, existingReturns, nil
}

func (c *Client) ensureConsumer(ctx context.Context, route RouteConfig) (Channel, error) {
	conn, err := c.ensureConnection(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.consumer != nil {
		channel := c.consumer
		c.mu.Unlock()
		return channel, nil
	}
	c.mu.Unlock()
	channel, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open consumer channel: %w", err)
	}
	if err := c.declareRoute(channel, route); err != nil {
		_ = channel.Close()
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = channel.Close()
		return nil, mq.ErrClosed
	}
	if c.consumer == nil {
		c.consumer = channel
		c.mu.Unlock()
		return channel, nil
	}
	existing := c.consumer
	c.mu.Unlock()
	_ = channel.Close()
	return existing, nil
}

func (c *Client) declareRoute(channel Channel, route RouteConfig) error {
	if !c.cfg.DeclareTopology {
		return nil
	}
	if route.Exchange != "" {
		if err := channel.ExchangeDeclare(route.Exchange, route.ExchangeType, route.Durable, route.AutoDelete, false, false, route.ExchangeArguments); err != nil {
			return fmt.Errorf("declare exchange %q: %w", route.Exchange, err)
		}
	}
	if route.Queue != "" {
		args := cloneTable(route.QueueArguments)
		if c.cfg.DeadLetter.Exchange != "" {
			if _, ok := args["x-dead-letter-exchange"]; !ok {
				args["x-dead-letter-exchange"] = c.cfg.DeadLetter.Exchange
			}
			if c.cfg.DeadLetter.RoutingKey != "" {
				if _, ok := args["x-dead-letter-routing-key"]; !ok {
					args["x-dead-letter-routing-key"] = c.cfg.DeadLetter.RoutingKey
				}
			}
		}
		if _, err := channel.QueueDeclare(route.Queue, route.Durable, route.AutoDelete, route.Exclusive, false, args); err != nil {
			return fmt.Errorf("declare queue %q: %w", route.Queue, err)
		}
	}
	for _, binding := range route.Bindings {
		if route.Exchange == "" {
			continue
		}
		queue := binding.Queue
		if queue == "" {
			queue = route.Queue
		}
		if err := channel.QueueBind(queue, binding.RoutingKey, route.Exchange, false, binding.Arguments); err != nil {
			return fmt.Errorf("bind queue %q to exchange %q: %w", queue, route.Exchange, err)
		}
	}
	return nil
}

func (c *Client) ensureConnection(ctx context.Context) (Connection, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, mq.ErrClosed
	}
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, mq.ErrClosed
	}
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.dialer(c.cfg.URL, c.tls)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, mq.ErrClosed
	}
	c.conn = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *Client) resetConnection() {
	c.mu.Lock()
	conn, publisher, consumer := c.conn, c.publisher, c.consumer
	c.conn, c.publisher, c.consumer = nil, nil, nil
	c.publisherReturns = nil
	c.mu.Unlock()
	if publisher != nil {
		_ = publisher.Close()
	}
	if consumer != nil && consumer != publisher {
		_ = consumer.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Client) resetPublisher() {
	c.mu.Lock()
	publisher := c.publisher
	c.publisher = nil
	c.publisherReturns = nil
	c.mu.Unlock()
	if publisher != nil {
		_ = publisher.Close()
	}
}

func (c *Client) startConsume(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is nil", mq.ErrInvalidArgument)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, mq.ErrClosed
	}
	if c.consumeCancel != nil {
		return nil, mq.ErrConsumerAlreadyRunning
	}
	consumeCtx, cancel := context.WithCancel(ctx)
	c.consumeCancel = cancel
	c.startOperationLocked()
	return consumeCtx, nil
}

func (c *Client) finishConsume() {
	c.mu.Lock()
	c.consumeCancel = nil
	c.mu.Unlock()
	c.endOperation()
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

func (c *Client) startOperationLocked() {
	if c.active == 0 {
		c.idle = make(chan struct{})
	}
	c.active++
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
	waitCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.cfg.Consumer.ShutdownTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, c.cfg.Consumer.ShutdownTimeout)
		defer cancel()
	}
	select {
	case <-idle:
	case <-waitCtx.Done():
		go c.closeAfterIdle(idle)
		return waitCtx.Err()
	}
	c.closeOnce.Do(c.closeResources)
	return c.closeErr
}

func (c *Client) closeAfterIdle(idle <-chan struct{}) {
	if idle != nil {
		<-idle
	}
	c.closeOnce.Do(c.closeResources)
}

func (c *Client) closeResources() {
	c.mu.Lock()
	conn, publisher, consumer := c.conn, c.publisher, c.consumer
	c.conn, c.publisher, c.consumer = nil, nil, nil
	c.mu.Unlock()
	var errs []error
	if publisher != nil {
		if err := publisher.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, fmt.Errorf("close publisher channel: %w", err))
		}
	}
	if consumer != nil && consumer != publisher {
		if err := consumer.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, fmt.Errorf("close consumer channel: %w", err))
		}
	}
	if conn != nil {
		if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, fmt.Errorf("close rabbitmq connection: %w", err))
		}
	}
	c.closeErr = errors.Join(errs...)
}

func waitConfirmation(ctx context.Context, confirmation PublishConfirmation, returns <-chan amqp.Return) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case returned, ok := <-returns:
			if ok {
				return fmt.Errorf("mandatory publish returned (code %d): %s", returned.ReplyCode, returned.ReplyText)
			}
			returns = nil
		case <-confirmation.Done():
			if !confirmation.Acked() {
				return errors.New("publisher negative acknowledgement")
			}
			select {
			case returned, ok := <-returns:
				if ok {
					return fmt.Errorf("mandatory publish returned (code %d): %s", returned.ReplyCode, returned.ReplyText)
				}
			default:
			}
			return nil
		}
	}
}

func waitBackoff(ctx context.Context, backoff time.Duration, attempt int) error {
	if attempt > 6 {
		attempt = 6
	}
	delay := backoff * time.Duration(attempt+1)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func firstError(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func isRecoverableConsumeError(err error) bool {
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		return amqpErr.Recover
	}
	return true
}

func publishRoute(route RouteConfig) bool { return route.Exchange != "" || route.Queue != "" }

func cloneTable(input amqp.Table) amqp.Table {
	if input == nil {
		return make(amqp.Table)
	}
	output := make(amqp.Table, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func headerSize(headers amqp.Table) int {
	var size int
	for key, value := range headers {
		size += len(key)
		switch value := value.(type) {
		case []byte:
			size += len(value)
		case string:
			size += len(value)
		}
	}
	return size
}

func isHandlerContext(ctx context.Context) bool {
	value, ok := ctx.Value(handlerContextKey{}).(bool)
	return ok && value
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func defaultDialer(rawURL string, tlsConfig *tls.Config) (Connection, error) {
	conn, err := amqp.DialTLS(rawURL, tlsConfig)
	if err != nil {
		return nil, err
	}
	return &amqpConnection{Connection: conn}, nil
}

type amqpConnection struct{ *amqp.Connection }

func (c *amqpConnection) Channel() (Channel, error) {
	channel, err := c.Connection.Channel()
	if err != nil {
		return nil, err
	}
	return &amqpChannel{Channel: channel}, nil
}

func (c *amqpConnection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return c.Connection.NotifyClose(receiver)
}

type amqpChannel struct{ *amqp.Channel }

func (c *amqpChannel) PublishWithDeferredConfirm(exchange, key string, mandatory, immediate bool, publishing amqp.Publishing) (PublishConfirmation, error) {
	confirmation, err := c.Channel.PublishWithDeferredConfirm(exchange, key, mandatory, immediate, publishing)
	if err != nil || confirmation == nil {
		return confirmation, err
	}
	return confirmation, nil
}

var _ PublishConfirmation = (*amqp.DeferredConfirmation)(nil)
