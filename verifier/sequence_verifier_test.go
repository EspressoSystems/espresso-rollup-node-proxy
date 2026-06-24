package verifier_test

import (
	"context"
	"fmt"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/verifier"
)

// SimpleIntVerifier is a simple implementation of the
// [verifier.SequenceVerifier] for a sequence of integers.
type SimpleIntVerifier struct {
	current int
}

// Compile time type check to ensure interface adherence.
var _ verifier.SequenceVerifier[int] = (*SimpleIntVerifier)(nil)

// VerifySuccessor implements [verifier.SequenceVerifier].
func (s *SimpleIntVerifier) VerifySuccessor(ctx context.Context, successor int) error {
	if successor != s.current+1 {
		return fmt.Errorf("expected %d, got %d", s.current+1, successor)
	}

	defer func() {
		s.current = successor
	}()

	return nil
}

// Example demonstrates how the expected behavior of the interface
// [verifier.SequenceVerifier] is expected to behave when performing a
// verification
// of a sequence.
//
// The idea is that is attempting to be conveyed is that the
// [verifier.SequenceVerifier] is itself stateful, and will automatically
// advance its state to the given valid successor.
func Example() {
	ctx := context.Background()
	verifier := &SimpleIntVerifier{current: 0}
	for i := range 10 {
		if err := verifier.VerifySuccessor(ctx, i); err != nil {
			fmt.Printf("Did not verify %d: %s\n", i, err)
			continue
		}

		fmt.Printf("Successfully verified %d\n", i)
	}

	// Output: Did not verify 0: expected 1, got 0
	// Successfully verified 1
	// Successfully verified 2
	// Successfully verified 3
	// Successfully verified 4
	// Successfully verified 5
	// Successfully verified 6
	// Successfully verified 7
	// Successfully verified 8
	// Successfully verified 9
}
