package store

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/google/renameio"
)

// AtomicPersistantFileSystemStore is a simple implementation of a persistent
// store that is is a FailableStorage, utilizing the file system to persist
// the state.
//
// The Writing of the state is expected to be atomic.
//
// NOTE: It is not guaranteed to be thread safe to perfrom Writes.
type AtomicPersistantFileSystemStore[T any] struct {
	filePath       string
	encoderCreator EncoderCreator[T]
	decoderCreator DecoderCreator[T]
}

// Compile-time assertion that *AtomicPersistantFileSystemStore[T] implements
// FailableStorage[T].
var _ FailableStorage[any] = (*AtomicPersistantFileSystemStore[any])(nil)

// Load reads the state from the disk, returning an error if the file
// is not found or if the state is invalid.
//
// NOTE: There is a scenario where the result could be valid while an error
// is returned.  This could occur if there was an error Closing the file
// being read.  It should be noted that this would likely cause other
// issues and would be indictative of a large problem.  In general, this
// is very unlikely to occur.
func (a *AtomicPersistantFileSystemStore[T]) Load(_ context.Context) (result StoreState[T], err error) {
	filePath := a.filePath
	decoderCreator := a.decoderCreator
	// Check if the file exists, if so load the state from the disk
	if _, err := os.Stat(filePath); err != nil {
		return result, fmt.Errorf("failed to check for file existence: %w", err)
	}

	file, err := os.OpenFile(filePath, os.O_RDONLY, os.FileMode(0o644))
	if err != nil {
		return result, fmt.Errorf("failed to state from file: %w", err)
	}

	defer func() {
		closeErr := file.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("file close failed with error: %w", closeErr)
			if err != nil {
				err = errors.Join(closeErr, err)
			}

			err = closeErr
		}
	}()

	decoder := decoderCreator(file)
	value, err := decoder.Decode()
	if err != nil {
		return result, fmt.Errorf("failed to decode state from file: %w", err)
	}

	return StoreState[T]{
		State:  value,
		Status: Valid,
	}, nil
}

// store writes the provided state to the disk, by first writing to
// a temporary file, then renaming the file.
//
// NOTE: This is only guaranteed to be atomic if the file system rename
// operation is guaranteed to be atomic.
// Should be the case on linux utilizing ext4/XFS.
func (a *AtomicPersistantFileSystemStore[T]) Store(_ context.Context, state T) error {
	filePath := a.filePath
	encoderCreator := a.encoderCreator
	pendingFile, err := renameio.TempFile("", filePath)
	if err != nil {
		return fmt.Errorf("failed to open file to write to: %w", err)
	}

	// Create a JSON encoder, wrapping the io.Writer
	encoder := encoderCreator(pendingFile)
	if encodeErr := encoder.Encode(state); encodeErr != nil {
		// Attempt to ensure that we close the file
		if err := pendingFile.Cleanup(); err != nil {
			return fmt.Errorf("failed to write to file: %w, failed to close file: %w", encodeErr, err)
		}
		return fmt.Errorf("failed to write to file: %w", encodeErr)
	}

	if err := pendingFile.CloseAtomicallyReplace(); err != nil {
		return fmt.Errorf("failed to atomically replace file: %w", err)
	}

	return nil
}
