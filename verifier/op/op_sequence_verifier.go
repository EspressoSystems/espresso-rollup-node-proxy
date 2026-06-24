package verifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// slogBlockSummary is a wrapper around [types.Block] that implements
// [slog.LogValuer] to provide a summary of the block's key fields for
// structured logging.
type slogBlockSummary types.Block

// LogValue implements [slog.LogValuer].
//
// This implementation presents a summary of the underlying [types.Block] value
// by representing itself as a [slog.GroupValue] containing the block number,
// hash, and parent hash.
func (b *slogBlockSummary) LogValue() slog.Value {
	block := (*types.Block)(b)
	return slog.GroupValue(
		slog.Any("number", block.Number().String()),
		slog.String("hash", block.Hash().Hex()),
		slog.String("parent", block.ParentHash().Hex()),
	)
}

// ErrNoBatchGivenToVerify is a sentinel error returned when the OP
// sequencer verifier is called with a nil batch.
var ErrNoBatchGivenToVerify = errors.New("no batch given to verify")

// ErrBatchMismatch is returned when the OP sequencer verifier detects a
// mismatch between two separate blocks that are expected to be be
// equivalent.
type ErrBatchMismatch struct {
	Have, Want *types.Block
}

// Error implements error
func (e *ErrBatchMismatch) Error() string {
	return fmt.Sprintf("batch mistmatch: have %s, want %s", e.Have.Hash().Hex(), e.Want.Hash().Hex())
}

// LogValue implements [slog.LogValuer].
//
// It contains a base message field (msg), and the have and want values of
// the underlying [types.Block] values.
func (e *ErrBatchMismatch) LogValue() slog.Value {
	have, want := e.Have, e.Want

	return slog.GroupValue(
		slog.String("msg", "batch mismatch"),
		slog.Any("have",
			(*slogBlockSummary)(have)),
		slog.Any("want", (*slogBlockSummary)(want)),
	)
}

// ErrHashMismatch is returned when two hashes do not match.
type ErrHashMismatch struct {
	Have, Want common.Hash
}

// Error implements error
func (e *ErrHashMismatch) Error() string {
	return fmt.Sprintf("hash mistmatch: have %s, want %s", e.Have.Hex(), e.Want.Hex())
}

// LogValue implements [slog.LogValuer].
//
// This implementation contains an error message field (msg), and two fields
// for the mismatched hashes (have, want want).
func (e *ErrHashMismatch) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg", "hash mismatch"),
		slog.String("have", e.Have.Hex()),
		slog.String("want", e.Want.Hex()),
	)
}

func OPVerifySuccessorBatch(
	ctx context.Context,
	sourceOfTruth ExecutionClient,
	current,
	next *types.Block,
) error {
	// Peek the next batch from the OP streamer without advancing it
	// No new batch to verify, just return
	if next == nil {
		return ErrNoBatchGivenToVerify
	}

	if current != nil && next.ParentHash() != current.Hash() {
		return &ErrHashMismatch{
			Have: next.ParentHash(),
			Want: current.Hash(),
		}
	}

	batchNumber := next.Number()

	// Fetch the corresponding block from the full node first; we need its body
	// to complete the reconstructed Espresso block below.
	truthBlock, err := sourceOfTruth.BlockByNumber(ctx, batchNumber)
	if err != nil {
		return err
	}

	if err := ensureBlocksMatch(next, truthBlock); err != nil {
		return fmt.Errorf("batch verification failed for batch number %d: %w", batchNumber, &ErrBatchMismatch{
			Have: next, Want: truthBlock,
		})
	}

	return nil
}
