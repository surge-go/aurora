package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

var errRecoverablePoll = errors.New("mq kafka recoverable poll error")

const recoverablePollBackoff = 250 * time.Millisecond

func defaultDialer(cfg Config, tlsConfig *tls.Config, onRevoked func(string, []int32)) (Transport, error) {
	return newFranzTransport(cfg, tlsConfig, onRevoked)
}

type franzTransport struct{ client *kgo.Client }

func newFranzTransport(cfg Config, tlsConfig *tls.Config, onRevoked func(string, []int32)) (*franzTransport, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordRetries(cfg.Producer.MaxRetries),
		kgo.ProduceRequestTimeout(cfg.Producer.Timeout),
	}
	if tlsConfig != nil {
		opts = append(opts, kgo.DialTLSConfig(tlsConfig))
	}
	if mechanism := saslMechanism(cfg.SASL); mechanism != nil {
		opts = append(opts, kgo.SASL(mechanism))
	}
	if cfg.Consumer.GroupID != "" {
		reset := kgo.NewOffset().AtStart()
		if cfg.Consumer.StartOffset == StartLatest {
			reset = kgo.NewOffset().AtEnd()
		}
		opts = append(opts,
			kgo.ConsumerGroup(cfg.Consumer.GroupID),
			kgo.DisableAutoCommit(),
			kgo.ConsumeResetOffset(reset),
			kgo.SessionTimeout(cfg.Consumer.SessionTimeout),
			kgo.RebalanceTimeout(cfg.Consumer.RebalanceTimeout),
			kgo.OnPartitionsRevoked(func(_ context.Context, _ *kgo.Client, revoked map[string][]int32) {
				for topic, partitions := range revoked {
					onRevoked(topic, partitions)
				}
			}),
			kgo.OnPartitionsLost(func(_ context.Context, _ *kgo.Client, lost map[string][]int32) {
				for topic, partitions := range lost {
					onRevoked(topic, partitions)
				}
			}),
		)
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &franzTransport{client: client}, nil
}

func (t *franzTransport) Produce(ctx context.Context, record *Record) error {
	kafkaRecord := &kgo.Record{
		Topic:     record.Topic,
		Key:       cloneBytes(record.Key),
		Value:     cloneBytes(record.Value),
		Timestamp: record.Timestamp,
		Headers:   make([]kgo.RecordHeader, len(record.Headers)),
	}
	for i, header := range record.Headers {
		kafkaRecord.Headers[i] = kgo.RecordHeader{Key: header.Key, Value: cloneBytes(header.Value)}
	}
	return t.client.ProduceSync(ctx, kafkaRecord).FirstErr()
}

func (t *franzTransport) Subscribe(topic string) { t.client.AddConsumeTopics(topic) }

func (t *franzTransport) Poll(ctx context.Context, maxRecords int) ([]*Record, error) {
	fetches := t.client.PollRecords(ctx, maxRecords)
	var records []*Record
	fetches.EachRecord(func(record *kgo.Record) {
		converted := &Record{
			Topic: record.Topic, Partition: record.Partition, Offset: record.Offset,
			Key: cloneBytes(record.Key), Value: cloneBytes(record.Value), Timestamp: record.Timestamp,
			LeaderEpoch: record.LeaderEpoch, native: record,
			Headers: make([]RecordHeader, len(record.Headers)),
		}
		for i, header := range record.Headers {
			converted.Headers[i] = RecordHeader{Key: header.Key, Value: cloneBytes(header.Value)}
		}
		records = append(records, converted)
	})
	var recoverable bool
	for _, fetchErr := range fetches.Errors() {
		if ctx.Err() != nil {
			return records, nil
		}
		if isRecoverableFetchError(fetchErr.Err) {
			recoverable = true
			continue
		}
		return records, fetchErr.Err
	}
	if recoverable {
		return records, errRecoverablePoll
	}
	return records, nil
}

func isRecoverableFetchError(err error) bool {
	var groupSession *kgo.ErrGroupSession
	var dataLoss *kgo.ErrDataLoss
	return errors.As(err, &groupSession) || errors.As(err, &dataLoss) || kerr.IsRetriable(err)
}

func (t *franzTransport) Commit(ctx context.Context, record *Record) error {
	native, ok := record.native.(*kgo.Record)
	if !ok {
		return errInvalidNativeRecord
	}
	return t.client.CommitRecords(ctx, native)
}

func (t *franzTransport) Close() { t.client.Close() }
