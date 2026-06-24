package atomic

import "sync/atomic"

// Value is a thin wrapper around the golang stdlib [atomic.Value] type to
// support generics.
type Value[T any] atomic.Value

func (v *Value[T]) CompareAndSwap(old, new T) bool {
	return (*atomic.Value)(v).CompareAndSwap(old, new)
}

func (v *Value[T]) Load() (result T) {
	value := (*atomic.Value)(v).Load()
	if value == nil {
		return result
	}

	if cast, castOK := value.(T); castOK {
		return cast
	}

	return
}

func (v *Value[T]) Store(new T) {
	(*atomic.Value)(v).Store(new)
}

func (v *Value[T]) Swap(new T) T {
	return (*atomic.Value)(v).Swap(new).(T)
}
