package store

import (
	"context"
	"sync"
)

// PersistenceConfirmedStore is a [Storage] implementation that wraps a
// persistent [FailableStorage] and an in-memory [Storage]. It ensures that
// the state is persisted to the persistent store before it is stored in the
// in-memory store.
//
// The primary rational for this type of [Storage] is that it ensures that
// we can reflect what is currently being persisted, while also providing
// fast access to the verified state.
type PersistenceConfirmedStore[T any] struct {
	persistentStore FailableStorage[T]
	inMemoryStore   Storage[T]
	once            sync.Once
}

var _ Storage[any] = (*PersistenceConfirmedStore[any])(nil)

// loadFromPersistence will attempt to load the current state from the
// persistent store. Any failure has the error being handled by returning an
// empty state.
//
// If the persistent state is loaded successfully, it will be written to the
// in memory store for faster access and reference.
//
// The load from persistence is only attempted once, as the only expected
// error is to be a file not found error.
func (p *PersistenceConfirmedStore[T]) loadFromPersistence(ctx context.Context) StoreState[T] {
	p.once.Do(func() {
		state, err := p.persistentStore.Load(ctx)
		if err != nil {
			return
		}

		// Store it into memory
		p.inMemoryStore.Store(ctx, state.State)
	})

	return p.inMemoryStore.Load(ctx)
}

// loadFromMemoryWithFallback will attempt to load the current state from the
// in memory store.  If the loaded state is not considered valid, it will
// fallback to the loadFromPersistence method to attempt to load the state
// from the persistent store.
func (p *PersistenceConfirmedStore[T]) loadFromMemoryWithFallback(ctx context.Context) StoreState[T] {
	state := p.inMemoryStore.Load(ctx)

	if state.Status != Valid {
		return p.loadFromPersistence(ctx)
	}

	return state
}

// storeToPersistence will attempt to store the provided state into the
// persistent store. If the persistent store fails with an error the error
// is handled by not updating the in memory store.
//
// If successful, the in memory store will be updated with the new state.
func (p *PersistenceConfirmedStore[T]) storeToPersistence(ctx context.Context, newState T) {
	if err := p.persistentStore.Store(ctx, newState); err != nil {
		// We won't store anything
		return
	}

	p.inMemoryStore.Store(ctx, newState)
}

// Load implements [Storage]
//
// This implementation attempts to return the state from the in memory store
// first for fast access. If the in memory store is not valid, it will fallback
// to attempting to retrieve the state from the disk based persistent store.
// If the persistent store fails, an empty state is returned.
func (p *PersistenceConfirmedStore[T]) Load(ctx context.Context) StoreState[T] {
	return p.loadFromMemoryWithFallback(ctx)
}

// Store implements [Storage]
//
// This method will attempt to write the given state to the persistent store
// first.  Upon success, it will update the in memory store to match the
// committed state.
func (p *PersistenceConfirmedStore[T]) Store(ctx context.Context, newState T) {
	p.storeToPersistence(ctx, newState)
}
