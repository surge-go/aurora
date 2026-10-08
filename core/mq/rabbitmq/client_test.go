package rabbitmq

import (
	"context"
	"crypto/tls"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/surge-go/aurora/core/mq"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "missing url", cfg: Config{Routes: map[string]RouteConfig{"events": {Queue: "q"}}}, want: "url is required"},
		{name: "invalid url", cfg: Config{URL: "http://rabbit", Routes: map[string]RouteConfig{"events": {Queue: "q"}}}, want: "amqp://"},
		{name: "missing route target", cfg: Config{URL: "amqp://rabbit", Routes: map[string]RouteConfig{"events": {}}}, want: "exchange or queue"},
		{name: "invalid failure", cfg: Config{URL: "amqp://rabbit", Routes: map[string]RouteConfig{"events": {Queue: "q"}}, Failure: FailureConfig{Mode: FailureReject}}, want: "dead letter exchange"},
		{name: "tls pair", cfg: Config{URL: "amqps://rabbit", Routes: map[string]RouteConfig{"events": {Queue: "q"}}, TLS: &TLSConfig{CertFile: "cert.pem"}}, want: "cert_file and key_file"},
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
	cfg := (Config{URL: "amqp://rabbit", Routes: map[string]RouteConfig{"events": {Queue: " q "}}}).normalized()
	if cfg.Publisher.Timeout != 30*time.Second || !cfg.Publisher.Confirm || !cfg.Publisher.Mandatory {
		t.Fatalf("publisher defaults = %+v", cfg.Publisher)
	}
	if cfg.Consumer.Prefetch != 10 || cfg.Consumer.Concurrency != 1 || cfg.Consumer.ReconnectAttempts != 3 {
		t.Fatalf("consumer defaults = %+v", cfg.Consumer)
	}
	if cfg.Routes["events"].Queue != "q" {
		t.Fatalf("route queue = %q", cfg.Routes["events"].Queue)
	}
}

func TestConfigRequiresSafeDefaultFailureDestination(t *testing.T) {
	cfg := Config{
		URL:    "amqp://rabbit",
		Routes: map[string]RouteConfig{"events": {Queue: "events"}},
	}
	err := cfg.Validate()
	if err == nil || !contains(err.Error(), "dead letter exchange") {
		t.Fatalf("Validate() error = %v, want dead letter requirement", err)
	}
}

func TestHeadersRoundTripPreservesOrderAndDuplicates(t *testing.T) {
	want := mq.Message{Key: []byte{0, 1, 2}, Headers: []mq.Header{
		{Key: "trace", Value: []byte("a")},
		{Key: "trace", Value: []byte("b")},
		{Key: "empty", Value: nil},
	}}
	table, err := encodeHeaders(want)
	if err != nil {
		t.Fatal(err)
	}
	headers, key, err := decodeHeaders(table)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(want.Key) || len(headers) != len(want.Headers) {
		t.Fatalf("decoded key/headers = %q/%+v", key, headers)
	}
	for i := range headers {
		if headers[i].Key != want.Headers[i].Key || string(headers[i].Value) != string(want.Headers[i].Value) {
			t.Fatalf("header %d = %+v, want %+v", i, headers[i], want.Headers[i])
		}
	}
}

func TestHeadersRejectReservedKeys(t *testing.T) {
	_, err := encodeHeaders(mq.Message{Headers: []mq.Header{{Key: headerEnvelopeKey, Value: []byte("bad")}}})
	if err == nil || !contains(err.Error(), "reserved") {
		t.Fatalf("encodeHeaders() error = %v, want reserved key error", err)
	}
}

func TestDecodeOrdinaryHeadersForHeadersExchange(t *testing.T) {
	headers, _, err := decodeHeaders(amqp.Table{
		"tenant":   []byte("blue"),
		"priority": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 || headers[0].Key != "priority" || headers[1].Key != "tenant" {
		t.Fatalf("decoded ordinary headers = %+v", headers)
	}
}

func TestPublishWaitsForConfirmAndCopiesMessage(t *testing.T) {
	channel := &fakeChannel{confirmation: newFakeConfirmation(true)}
	connection := &fakeConnection{channel: channel}
	client, err := NewClient(&Config{
		URL:     "amqp://rabbit",
		Routes:  map[string]RouteConfig{"events": {Exchange: "events", RoutingKey: "created"}},
		Failure: FailureConfig{Mode: FailureReject, AllowDrop: true},
		Dialer:  func(string, *tls.Config) (Connection, error) { return connection, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	err = client.Publish(context.Background(), "events", mq.Message{ID: "id-1", Body: body, Headers: []mq.Header{{Key: "trace", Value: []byte("id")}}})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	body[0] = 'X'
	if string(channel.publishing.Body) != "payload" {
		t.Fatalf("published body aliases caller: %q", channel.publishing.Body)
	}
	if !channel.confirmCalled || channel.publishing.MessageId != "id-1" {
		t.Fatalf("publishing = %+v, confirm=%v", channel.publishing, channel.confirmCalled)
	}
}

func TestPublishReturnsMandatoryReturn(t *testing.T) {
	channel := &fakeChannel{confirmation: newFakeConfirmation(true), publishReturn: &amqp.Return{ReplyCode: 312, ReplyText: "NO_ROUTE"}}
	connection := &fakeConnection{channel: channel}
	client, err := NewClient(&Config{
		URL:     "amqp://rabbit",
		Routes:  map[string]RouteConfig{"events": {Exchange: "events", RoutingKey: "missing"}},
		Failure: FailureConfig{Mode: FailureReject, AllowDrop: true},
		Dialer:  func(string, *tls.Config) (Connection, error) { return connection, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Publish(context.Background(), "events", mq.Message{Body: []byte("payload")})
	if err == nil || !contains(err.Error(), "mandatory publish returned") {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestPublishTimeoutClosesPublisherChannel(t *testing.T) {
	channel := &fakeChannel{confirmation: &fakeConfirmation{done: make(chan struct{})}}
	connection := &fakeConnection{channel: channel}
	client, err := NewClient(&Config{
		URL:       "amqp://rabbit",
		Routes:    map[string]RouteConfig{"events": {Exchange: "events", RoutingKey: "created"}},
		Publisher: PublisherConfig{Timeout: time.Millisecond},
		Failure:   FailureConfig{Mode: FailureReject, AllowDrop: true},
		Dialer:    func(string, *tls.Config) (Connection, error) { return connection, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Publish(context.Background(), "events", mq.Message{Body: []byte("payload")})
	if err == nil || !contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Publish() error = %v, want timeout", err)
	}
	if channel.closeCount != 1 {
		t.Fatalf("publisher close count = %d, want 1", channel.closeCount)
	}
}

func TestCloseTimeoutSchedulesResourceCleanup(t *testing.T) {
	deliveries := make(chan amqp.Delivery, 1)
	ack := &fakeAcknowledger{}
	deliveries <- amqp.Delivery{Acknowledger: ack, MessageId: "id", Body: []byte("body")}
	channel := &fakeChannel{deliveries: deliveries}
	connection := &fakeConnection{channel: channel}
	client, err := NewClient(&Config{
		URL:      "amqp://rabbit",
		Routes:   map[string]RouteConfig{"events": {Queue: "events"}},
		Consumer: ConsumerConfig{ShutdownTimeout: 5 * time.Millisecond},
		Failure:  FailureConfig{Mode: FailureReject, AllowDrop: true},
		Dialer:   func(string, *tls.Config) (Connection, error) { return connection, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- client.Consume(context.Background(), "events", func(context.Context, mq.Message) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if err := client.Close(context.Background()); err == nil {
		t.Fatal("Close() error = nil, want shutdown timeout")
	}
	close(release)
	select {
	case <-consumeDone:
	case <-time.After(time.Second):
		t.Fatal("Consume() did not stop")
	}
	deadline := time.Now().Add(time.Second)
	for channel.closedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if channel.closedCount() == 0 {
		t.Fatal("resource cleanup did not close the channel")
	}
}

func TestNonRecoverableAMQPErrorIsNotRetryable(t *testing.T) {
	if isRecoverableConsumeError(&amqp.Error{Code: amqp.AccessRefused, Recover: false}) {
		t.Fatal("non-recoverable AMQP error was marked recoverable")
	}
	if !isRecoverableConsumeError(&amqp.Error{Code: amqp.InternalError, Recover: true}) {
		t.Fatal("recoverable AMQP error was marked non-recoverable")
	}
}

func TestConsumeAcknowledgesIndependentMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ack := &fakeAcknowledger{}
	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- amqp.Delivery{Acknowledger: ack, MessageId: "id", Body: []byte("body"), Headers: mustHeaders(t, mq.Message{Headers: []mq.Header{{Key: "trace", Value: []byte("v")}}})}
	channel := &fakeChannel{deliveries: deliveries}
	connection := &fakeConnection{channel: channel}
	client, err := NewClient(&Config{
		URL:     "amqp://rabbit",
		Routes:  map[string]RouteConfig{"events": {Exchange: "events", Queue: "events", Bindings: []BindingConfig{{RoutingKey: "events"}}}},
		Failure: FailureConfig{Mode: FailureReject, AllowDrop: true},
		Dialer:  func(string, *tls.Config) (Connection, error) { return connection, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- client.Consume(ctx, "events", func(handlerCtx context.Context, message mq.Message) error {
			if string(message.Body) != "body" || string(message.Headers[0].Value) != "v" {
				t.Errorf("message = %+v", message)
			}
			message.Body[0] = 'X'
			cancel()
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Consume() did not stop")
	}
	if ack.ackCount != 1 || ack.nacks != 0 {
		t.Fatalf("acks=%d nacks=%d", ack.ackCount, ack.nacks)
	}
}

type fakeConnection struct {
	channel Channel
}

func (c *fakeConnection) Channel() (Channel, error)                        { return c.channel, nil }
func (c *fakeConnection) NotifyClose(ch chan *amqp.Error) chan *amqp.Error { return ch }
func (c *fakeConnection) Close() error                                     { return nil }

type fakeChannel struct {
	mu            sync.Mutex
	confirmation  PublishConfirmation
	publishing    amqp.Publishing
	confirmCalled bool
	returns       chan amqp.Return
	publishReturn *amqp.Return
	deliveries    <-chan amqp.Delivery
	closeCount    int
}

func (c *fakeChannel) Close() error { c.mu.Lock(); defer c.mu.Unlock(); c.closeCount++; return nil }
func (c *fakeChannel) closedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}
func (c *fakeChannel) Confirm(bool) error { c.confirmCalled = true; return nil }
func (c *fakeChannel) PublishWithDeferredConfirm(_ string, _ string, _ bool, _ bool, publishing amqp.Publishing) (PublishConfirmation, error) {
	c.mu.Lock()
	c.publishing = publishing
	returnConfirmation := c.confirmation
	ret := c.publishReturn
	returns := c.returns
	c.mu.Unlock()
	if ret != nil && returns != nil {
		returns <- *ret
	}
	return returnConfirmation, nil
}
func (c *fakeChannel) NotifyReturn(ch chan amqp.Return) chan amqp.Return { c.returns = ch; return ch }
func (c *fakeChannel) ExchangeDeclare(string, string, bool, bool, bool, bool, amqp.Table) error {
	return nil
}
func (c *fakeChannel) QueueDeclare(string, bool, bool, bool, bool, amqp.Table) (amqp.Queue, error) {
	return amqp.Queue{}, nil
}
func (c *fakeChannel) QueueBind(string, string, string, bool, amqp.Table) error { return nil }
func (c *fakeChannel) Qos(int, int, bool) error                                 { return nil }
func (c *fakeChannel) ConsumeWithContext(context.Context, string, string, bool, bool, bool, bool, amqp.Table) (<-chan amqp.Delivery, error) {
	return c.deliveries, nil
}

type fakeConfirmation struct {
	done chan struct{}
	ack  bool
}

func newFakeConfirmation(ack bool) *fakeConfirmation {
	c := &fakeConfirmation{done: make(chan struct{}), ack: ack}
	close(c.done)
	return c
}
func (c *fakeConfirmation) Done() <-chan struct{} { return c.done }
func (c *fakeConfirmation) Acked() bool           { return c.ack }

type fakeAcknowledger struct {
	mu       sync.Mutex
	ackCount int
	nacks    int
}

func (a *fakeAcknowledger) Ack(uint64, bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ackCount++
	return nil
}
func (a *fakeAcknowledger) Nack(uint64, bool, bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nacks++
	return nil
}
func (a *fakeAcknowledger) Reject(uint64, bool) error { return nil }

func mustHeaders(t *testing.T, message mq.Message) amqp.Table {
	t.Helper()
	headers, err := encodeHeaders(message)
	if err != nil {
		t.Fatal(err)
	}
	return headers
}

func contains(value, want string) bool {
	return len(value) >= len(want) && strings.Contains(value, want)
}
