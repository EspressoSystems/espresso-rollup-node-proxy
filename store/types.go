package store

import (
	"context"
	"io"
)

// Encoder is a generic interface for encoding some underlying state `T` via
// some method.
type Encoder[T any] interface {
	// Encode encodes the provided value `T` via some method.  Any failure to
	// encode the value should result in an error being returned.
	Encode(value T) error
}

// Decoder is a generic interface for decoding some underlying state `T` via
// some method.
type Decoder[T any] interface {
	// Decode decodes the underlying value `T` via some method.  Any failure to
	// decode the value should result in an error being returned.  If no error
	// is returned, then the returned value should be considered valid.
	Decode() (T, error)
}

type (
	// EncoderCreator is a function type that creates an [Encoder] for some type
	// `T` given an [io.Writer].
	EncoderCreator[T any] func(w io.Writer) Encoder[T]

	// DecoderCreator is a function type that creates a [Decoder] for some type
	// `T` given an [io.Reader].
	DecoderCreator[T any] func(r io.Reader) Decoder[T]
)

type ValidityStatus int

const (
	Invalid ValidityStatus = iota
	Valid
)

// StoreState is a wrapper around the underlying state `T` that adds context
// of whether the value is valid or not.
//
// This adds the context that the underlying stored state is valid (IE it is
// informed by something concrete) or not.
type StoreState[T any] struct {
	State  T
	Status ValidityStatus
}

// Storage is a generic interface for storing and retrieving some underlying
// state.
type Storage[T any] interface {
	// Load utilizes the Storage layer to retrieve the current Stored object.
	// If nothing has been stored, then this may return an invalid state.  As
	// a result, this does not return the state `T` itself, but rather a
	// [StoreState] that contains the underlying object.
	//
	// This [StoreState] adds context of whether the value retrieved is valid
	// or not.
	Load(ctx context.Context) StoreState[T]

	// Store utilizties the Storage layer to persist the provided state.
	Store(ctx context.Context, newState T)
}

// FailableStorage is a generic interface for storing and retrieving some
// underlying data in a way that is failable.  This is an extension to the
// [Storage] interface that adds errors to the [Storage.Load] and
// [Storage.Store] methods.
type FailableStorage[T any] interface {
	// Load utilizes the Storage layer to retrieve the current Stored object.
	// Any failure to retrieve the stored value will result in an error being
	// returned.
	//
	// The returned [StoreState] may still be invalid even without an error.
	Load(ctx context.Context) (StoreState[T], error)

	// Store utilizties the Storage layer to persist the provided state.  Any
	// failure to persist the state will result in an error being returned.
	Store(ctx context.Context, state T) error
}
