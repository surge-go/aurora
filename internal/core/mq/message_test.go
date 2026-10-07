package mq

import "testing"

func TestMessageCloneCopiesMutableFields(t *testing.T) {
	original := Message{
		ID:      "message-1",
		Key:     []byte("key"),
		Body:    []byte("body"),
		Headers: []Header{{Key: "trace", Value: []byte("trace-id")}},
	}

	clone := original.Clone()
	clone.Key[0] = 'K'
	clone.Body[0] = 'B'
	clone.Headers[0].Value[0] = 'T'
	clone.Headers[0].Key = "changed"

	if string(original.Key) != "key" {
		t.Fatalf("original key changed: %q", original.Key)
	}
	if string(original.Body) != "body" {
		t.Fatalf("original body changed: %q", original.Body)
	}
	if original.Headers[0].Key != "trace" || string(original.Headers[0].Value) != "trace-id" {
		t.Fatalf("original headers changed: %+v", original.Headers)
	}
}

func TestMessageClonePreservesNilAndEmptySlices(t *testing.T) {
	var nilMessage Message
	if got := nilMessage.Clone(); got.Key != nil || got.Body != nil || got.Headers != nil {
		t.Fatalf("Clone() changed nil slices: %+v", got)
	}

	emptyMessage := Message{
		Key:     []byte{},
		Body:    []byte{},
		Headers: []Header{},
	}
	clone := emptyMessage.Clone()
	if clone.Key == nil || clone.Body == nil || clone.Headers == nil {
		t.Fatalf("Clone() changed empty slices to nil: %+v", clone)
	}
}
