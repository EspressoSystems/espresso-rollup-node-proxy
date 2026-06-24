package store

import (
	"context"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/atomic"
)

// InMemoryStorage is a simple implementation of Storage that keeps the state
// in memory. It is not persistent but it is thread-safe.
type InMemoryStorage[T any] atomic.Value[StoreState[T]]

// Compile-time assertion that *InMemoryStorage[T] implements Storage[T].
var _ Storage[any] = (*InMemoryStorage[any])(nil)

// Load implements [Storage].
func (i *InMemoryStorage[T]) Load(_ context.Context) StoreState[T] {
	return (*atomic.Value[StoreState[T]])(i).Load()
}

// Store implements [Storage].
func (i *InMemoryStorage[T]) Store(_ context.Context, newState T) {
	(*atomic.Value[StoreState[T]])(i).Store(StoreState[T]{
		State:  newState,
		Status: Valid,
	})
}

// NewInMemoryStorage creates a [Storage] instance that is stored
// in-memory, non-failable, non-persistent, and thread-safe.
func NewInMemoryStorage[T any]() Storage[T] {
	return new(InMemoryStorage[T])
}
