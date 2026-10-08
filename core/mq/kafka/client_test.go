package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/surge-go/aurora/core/mq"
)

type fakeTransport struct {
	mu          sync.Mutex
	produced    []*Record
	committed   []*Record
	pollRecords chan *Record
	produceErr  error
	commitErr   error
	pollErr     error
	closed      bool
	subscribed  string
	onRevoke    func(string, []int32)
	blockCommit bool
	commitStart chan struct{}
	commitOnce  sync.Once
}

func newFakeTransport() *fakeTransport { return &fakeTransport{pollRecords: make(chan *Record, 16)} }
func (f *fakeTransport) Produce(_ context.Context, record *Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.produceErr != nil {
		return f.produceErr
	}
	copyRecord := *record
	copyRecord.Key, copyRecord.Value = cloneBytes(record.Key), cloneBytes(record.Value)
	f.produced = append(f.produced, &copyRecord)
	return nil
}
func (f *fakeTransport) Subscribe(topic string) { f.mu.Lock(); f.subscribed = topic; f.mu.Unlock() }
func (f *fakeTransport) Poll(ctx context.Context, max int) ([]*Record, error) {
	select {
	case <-ctx.Done():
		return nil, nil
	case record := <-f.pollRecords:
		f.mu.Lock()
		err := f.pollErr
		f.pollErr = nil
		f.mu.Unlock()
		return []*Record{record}, err
	}
}
func (f *fakeTransport) Commit(ctx context.Context, record *Record) error {
	f.mu.Lock()
	commitErr, blockCommit, commitStart := f.commitErr, f.blockCommit, f.commitStart
	f.mu.Unlock()
	if blockCommit {
		f.commitOnce.Do(func() { close(commitStart) })
		<-ctx.Done()
		return ctx.Err()
	}
	if commitErr != nil {
		return commitErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, record)
	return nil
}
func (f *fakeTransport) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func newTestClient(t *testing.T, transport *fakeTransport, config func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Brokers: []string{"localhost:9092"}, ClientID: "test-client",
		Routes:   map[string]RouteConfig{"events": {Topic: "events"}, "dead": {Topic: "dead-events"}},
		Consumer: ConsumerConfig{GroupID: "test-group"},
		Dialer: func(_ Config, _ *tls.Config, onRevoke func(string, []int32)) (Transport, error) {
			transport.onRevoke = onRevoke
			return transport, nil
		},
	}
	if config != nil {
		config(&cfg)
	}
	client, err := NewClient(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name   string
		update func(*Config)
		want   string
	}{
		{"empty broker", func(c *Config) { c.Brokers = nil }, "brokers are required"},
		{"invalid broker", func(c *Config) { c.Brokers = []string{"kafka:bad:9092"} }, "host:port"},
		{"invalid topic", func(c *Config) { c.Routes["events"] = RouteConfig{Topic: "bad topic"} }, "invalid topic"},
		{"invalid retry", func(c *Config) { c.Consumer.Failure.Mode = FailureRetry }, "requires positive max_attempts"},
		{"unsafe drop", func(c *Config) { c.Consumer.Failure.Mode = FailureDrop }, "requires allow_drop"},
		{"dead letter self route", func(c *Config) {
			c.Consumer.Failure = FailureConfig{Mode: FailureDeadLetter, DeadLetterRoute: "events"}
		}, "at least one source route"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Brokers: []string{"localhost:9092"}, ClientID: "client", Routes: map[string]RouteConfig{"events": {Topic: "events"}}, Consumer: ConsumerConfig{GroupID: "group"}, Dialer: func(Config, *tls.Config, func(string, []int32)) (Transport, error) { return newFakeTransport(), nil }}
			test.update(&cfg)
			if test.name == "empty broker" {
				cfg.Dialer = nil
			}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestConfigAcceptsSeparateDeadLetterRoute(t *testing.T) {
	cfg := Config{
		Brokers: []string{"localhost:9092"}, ClientID: "client",
		Routes:   map[string]RouteConfig{"events": {Topic: "events"}, "dead": {Topic: "dead-events"}},
		Consumer: ConsumerConfig{GroupID: "group", Failure: FailureConfig{Mode: FailureDeadLetter, DeadLetterRoute: "dead"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRecordRoundTripPreservesHeadersAndCopies(t *testing.T) {
	want := mq.Message{ID: "event-1", Key: []byte{0, 1}, Body: []byte("body"), Timestamp: time.Unix(123, 0), Headers: []mq.Header{{Key: "trace", Value: []byte("a")}, {Key: "trace", Value: []byte("b")}}}
	record, err := encodeRecord("events", want)
	if err != nil {
		t.Fatal(err)
	}
	want.Body[0] = 'X'
	if string(record.Value) != "body" {
		t.Fatalf("record aliases caller body: %q", record.Value)
	}
	got, err := decodeRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "event-1" || string(got.Key) != string([]byte{0, 1}) || string(got.Body) != "body" || !got.Timestamp.Equal(time.Unix(123, 0)) {
		t.Fatalf("decoded message = %+v", got)
	}
	if len(got.Headers) != 2 || got.Headers[0].Key != "trace" || string(got.Headers[1].Value) != "b" {
		t.Fatalf("decoded headers = %+v", got.Headers)
	}
}

func TestEncodeRecordRejectsReservedHeader(t *testing.T) {
	_, err := encodeRecord("events", mq.Message{Headers: []mq.Header{{Key: messageIDHeader}}})
	if !errors.Is(err, mq.ErrInvalidArgument) {
		t.Fatalf("encodeRecord() error = %v", err)
	}
}

func TestPublishWaitsForTransportAndEnforcesMaxPayload(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, nil)
	body := []byte("body")
	if err := client.Publish(context.Background(), "events", mq.Message{ID: "id", Body: body}); err != nil {
		t.Fatal(err)
	}
	body[0] = 'X'
	if len(transport.produced) != 1 || string(transport.produced[0].Value) != "body" {
		t.Fatalf("produced records = %+v", transport.produced)
	}
	limited := newTestClient(t, newFakeTransport(), func(c *Config) { c.MaxPayload = 1 })
	if err := limited.Publish(context.Background(), "events", mq.Message{Body: []byte("too large")}); !errors.Is(err, mq.ErrInvalidArgument) {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestConsumeCommitsOnlyAfterSuccessfulHandler(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, nil)
	ctx, cancel := context.WithCancel(context.Background())
	transport.pollRecords <- &Record{Topic: "events", Partition: 2, Offset: 41, Value: []byte("ok")}
	done := make(chan error, 1)
	go func() {
		done <- client.Consume(ctx, "events", func(_ context.Context, message mq.Message) error {
			if string(message.Body) != "ok" {
				t.Errorf("body = %q", message.Body)
			}
			return nil
		})
	}()
	waitFor(t, func() bool { transport.mu.Lock(); defer transport.mu.Unlock(); return len(transport.committed) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.subscribed != "events" || len(transport.committed) != 1 || transport.committed[0].Offset != 41 {
		t.Fatalf("subscribed=%q committed=%+v", transport.subscribed, transport.committed)
	}
}

func TestConsumeProcessesRecordsBeforeRecoverablePollError(t *testing.T) {
	transport := newFakeTransport()
	transport.pollErr = errRecoverablePoll
	client := newTestClient(t, transport, nil)
	ctx, cancel := context.WithCancel(context.Background())
	transport.pollRecords <- &Record{Topic: "events", Partition: 1, Offset: 21, Value: []byte("kept")}
	done := make(chan error, 1)
	var handled int
	go func() {
		done <- client.Consume(ctx, "events", func(_ context.Context, message mq.Message) error {
			if string(message.Body) != "kept" {
				t.Errorf("body = %q", message.Body)
			}
			handled++
			return nil
		})
	}()
	waitFor(t, func() bool { transport.mu.Lock(); defer transport.mu.Unlock(); return len(transport.committed) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if handled != 1 {
		t.Fatalf("handler calls = %d, want 1", handled)
	}
}

func TestConsumeDrainsRecordsBeforeFatalPollError(t *testing.T) {
	transport := newFakeTransport()
	transport.pollErr = errors.New("fatal fetch error")
	client := newTestClient(t, transport, nil)
	transport.pollRecords <- &Record{Topic: "events", Partition: 1, Offset: 22, Value: []byte("drain")}
	err := client.Consume(context.Background(), "events", func(context.Context, mq.Message) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "fatal fetch error") {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.committed) != 1 {
		t.Fatalf("commits = %d, want record drained before fatal error", len(transport.committed))
	}
}

func TestConsumeHandlerErrorDoesNotCommit(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, nil)
	transport.pollRecords <- &Record{Topic: "events", Partition: 0, Offset: 8, Value: []byte("bad")}
	err := client.Consume(context.Background(), "events", func(context.Context, mq.Message) error { return errors.New("failed") })
	if err == nil || !strings.Contains(err.Error(), "handler failed") {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.committed) != 0 {
		t.Fatalf("commits = %+v", transport.committed)
	}
}

func TestConsumeRetriesOnlyConfiguredAttempts(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, func(c *Config) {
		c.Consumer.Retry = RetryConfig{MaxAttempts: 3, Backoff: time.Millisecond}
		c.Consumer.Failure.Mode = FailureRetry
	})
	transport.pollRecords <- &Record{Topic: "events", Partition: 0, Offset: 8, Value: []byte("retry")}
	var attempts int
	err := client.Consume(context.Background(), "events", func(context.Context, mq.Message) error {
		attempts++
		return errors.New("retry me")
	})
	if err == nil || attempts != 3 {
		t.Fatalf("Consume() error=%v attempts=%d, want 3", err, attempts)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.committed) != 0 {
		t.Fatalf("committed records = %+v", transport.committed)
	}
}

func TestConsumeCommitFailureReturnsError(t *testing.T) {
	transport := newFakeTransport()
	transport.commitErr = errors.New("commit rejected")
	client := newTestClient(t, transport, nil)
	transport.pollRecords <- &Record{Topic: "events", Partition: 0, Offset: 8, Value: []byte("ok")}
	err := client.Consume(context.Background(), "events", func(context.Context, mq.Message) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "commit rejected") {
		t.Fatalf("Consume() error = %v", err)
	}
}

func TestRevokeCancelsInFlightCommitBeforeReturning(t *testing.T) {
	transport := newFakeTransport()
	transport.blockCommit = true
	transport.commitStart = make(chan struct{})
	client := newTestClient(t, transport, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.pollRecords <- &Record{Topic: "events", Partition: 3, Offset: 12, Value: []byte("ok")}
	done := make(chan error, 1)
	go func() { done <- client.Consume(ctx, "events", func(context.Context, mq.Message) error { return nil }) }()
	select {
	case <-transport.commitStart:
	case <-time.After(time.Second):
		t.Fatal("commit did not start")
	}
	revoked := make(chan struct{})
	go func() { transport.onRevoke("events", []int32{3}); close(revoked) }()
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revoke did not cancel the in-flight commit")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.committed) != 0 {
		t.Fatalf("committed records = %+v", transport.committed)
	}
}

func TestConsumeDeadLettersBeforeCommit(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, func(c *Config) {
		c.Consumer.Failure = FailureConfig{Mode: FailureDeadLetter, DeadLetterRoute: "dead"}
	})
	transport.pollRecords <- &Record{Topic: "events", Partition: 1, Offset: 9, Value: []byte("bad")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Consume(ctx, "events", func(context.Context, mq.Message) error { return errors.New("private error detail") })
	}()
	waitFor(t, func() bool { transport.mu.Lock(); defer transport.mu.Unlock(); return len(transport.committed) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.produced) != 1 || transport.produced[0].Topic != "dead-events" {
		t.Fatalf("produced = %+v", transport.produced)
	}
	if string(transport.produced[0].Value) != "bad" {
		t.Fatalf("DLQ body = %q", transport.produced[0].Value)
	}
	for _, header := range transport.produced[0].Headers {
		if strings.Contains(string(header.Value), "private error detail") {
			t.Fatal("handler error text leaked into DLQ")
		}
	}
}

func TestCloseCancelsHandlerAndClosesTransport(t *testing.T) {
	transport := newFakeTransport()
	client := newTestClient(t, transport, nil)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- client.Consume(context.Background(), "events", func(ctx context.Context, _ mq.Message) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	transport.pollRecords <- &Record{Topic: "events", Partition: 0, Offset: 1}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if !transport.closed {
		t.Fatal("transport was not closed")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
