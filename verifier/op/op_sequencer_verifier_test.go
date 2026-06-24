package verifier_test

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"

	verifier "github.com/EspressoSystems/espresso-rollup-node-proxy/verifier/op"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestErrHashMismatch_ErrorsAs(t *testing.T) {
	require := require.New(t)
	err := fmt.Errorf("this is a wrapped error: %w", &verifier.ErrHashMismatch{
		Have: common.Hash{20: 0x01},
		Want: common.Hash{20: 0x02},
	})

	mismatch := new(verifier.ErrHashMismatch)
	require.True(errors.As(err, &mismatch))
	require.Equal(common.Hash{20: 0x01}, mismatch.Have)
	require.Equal(common.Hash{20: 0x02}, mismatch.Want)
}

func ExampleErrHashMismatch() {
	slog.Info("encountered error", "err", &verifier.ErrHashMismatch{
		Have: common.Hash{20: 0x01},
		Want: common.Hash{20: 0x02},
	})
}
