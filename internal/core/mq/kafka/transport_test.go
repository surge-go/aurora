package kafka

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestRecoverableFetchErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "group session", err: &kgo.ErrGroupSession{Err: errors.New("coordinator unavailable")}, want: true},
		{name: "data loss notification", err: &kgo.ErrDataLoss{Topic: "events", Partition: 0}, want: true},
		{name: "retriable broker error", err: kerr.LeaderNotAvailable, want: true},
		{name: "non-retriable broker error", err: kerr.InvalidTopicException, want: false},
		{name: "untyped error", err: errors.New("decode failure"), want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isRecoverableFetchError(test.err); got != test.want {
				t.Fatalf("isRecoverableFetchError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
