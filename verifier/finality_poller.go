package verifier

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

const (
	defaultFinalityPollInterval = time.Second

	finalityPollTimeout = 5 * time.Second
)

// FinalityPoller is the finality poller as consumed by a verifier. T is the
// verifier-specific snapshot type: the OP verifier uses a struct carrying
// the finalized L2 and L1 blocks, while the Nitro verifier uses a plain
// uint64 block number.
type FinalityPoller[T any] interface {
	// LastSnapshot returns the most recently polled snapshot. The bool is false if
	// the poller has not successfully fetched any snapshot yet.
	LastSnapshot() (snapshot T, isValid bool)
}

// FinalityPollerEager is a FinalityPoller that actively polls for finality
// updates at a regular interval via a background goroutine. The
// [FinalityPollerEager.Start] and [FinalityPollerEager.Stop] methods control
// the lifecycle of the background goroutine.
type FinalityPollerEager[T any] interface {
	FinalityPoller[T]
	Start(ctx context.Context)
	Stop()
}

type FinalityPollerConfig[T any] struct {
	finalityPollFunc func(ctx context.Context) (T, error)
	logger           log.Logger
	interval         time.Duration
}

type FinalityPollerOption[T any] func(c *FinalityPollerConfig[T])

func WithFinalityPollFunc[T any](f func(ctx context.Context) (T, error)) FinalityPollerOption[T] {
	return func(c *FinalityPollerConfig[T]) {
		c.finalityPollFunc = f
	}
}

func WithLogger[T any](logger log.Logger) FinalityPollerOption[T] {
	return func(c *FinalityPollerConfig[T]) {
		c.logger = logger
	}
}

func WithInterval[T any](interval time.Duration) FinalityPollerOption[T] {
	return func(c *FinalityPollerConfig[T]) {
		c.interval = interval
	}
}

func configValidation[T any](config *FinalityPollerConfig[T]) error {
	if config.interval == 0 {
		config.interval = defaultFinalityPollInterval
	}
	if config.logger == nil {
		config.logger = log.Root()
	}
	if config.finalityPollFunc == nil {
		return errors.New("we need a finality poll function to create a FinalityPoller")
	}

	return nil
}
