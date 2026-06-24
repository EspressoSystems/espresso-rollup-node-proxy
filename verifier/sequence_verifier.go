package verifier

import (
	"context"
)

// SequenceVerifier is the interface for a verifier that can verify a "next"
// item of a given type `T` is a valid successor to the current state.
type SequenceVerifier[T any] interface {
	// VerifySuccessor verifies the provided "successor" item of type `T`. Any
	// failure to verify the given next item should result in an error being
	// returned.
	//
	// If the [SequenceVerifier] is meant to verify a sequence, then any
	// successful verification should update any internal reference material.
	// Additionally The sequence verification should have some internal
	// reference material that it's able to advance.
	VerifySuccessor(ctx context.Context, successor T) error
}
