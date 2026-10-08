package mq

import "errors"

var (
	// ErrInvalidArgument indicates an invalid message, destination, or option.
	ErrInvalidArgument = errors.New("mq: invalid argument")

	// ErrClosed indicates that an operation was attempted after Close.
	ErrClosed = errors.New("mq: client is closed")

	// ErrConsumerAlreadyRunning indicates a second active Consume call.
	ErrConsumerAlreadyRunning = errors.New("mq: consumer is already running")
)
