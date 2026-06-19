package verifier

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

func NewFinalityPollerLegacy[T any](
	finalityPollFunc func(ctx context.Context) (T, error),
	logger log.Logger,
	interval time.Duration,
) FinalityPollerEager[T] {
	return &noopEagerFinalityPoller[T]{
		FinalityPoller: NewFinalityPollerLazy(

			WithFinalityPollFunc(finalityPollFunc),
			WithLogger[T](logger),
			WithInterval[T](interval),
		),
	}
}

type noopEagerFinalityPoller[T any] struct {
	FinalityPoller[T]
}

var _ FinalityPollerEager[any] = (*noopEagerFinalityPoller[any])(nil)

// Start implements [FinalityPollerEager].
func (n *noopEagerFinalityPoller[T]) Start(ctx context.Context) {
}

// Stop implements [FinalityPollerEager].
func (n *noopEagerFinalityPoller[T]) Stop() {
}
