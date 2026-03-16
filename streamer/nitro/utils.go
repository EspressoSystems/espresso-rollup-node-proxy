package nitro

import (
	"encoding/binary"
	"errors"

	"github.com/ccoveille/go-safecast"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

const (
	FilterAndFind_Remove = iota
	FilterAndFind_Keep
	FilterAndFind_Target
)

const MAX_ATTESTATION_QUOTE_SIZE int = 4 * 1024
const LEN_SIZE int = 8
const INDEX_SIZE int = 8

func BuildRawHotShotPayload(
	msgPositions []MessageIndex,
	msgFetcher func(MessageIndex) ([]byte, error),
	maxSize int64,
) ([]byte, int) {

	payload := []byte{}
	msgCnt := 0

	for _, p := range msgPositions {
		msgBytes, err := msgFetcher(p)
		if err != nil {
			log.Warn("failed to fetch the message", "pos", p)
			break
		}

		sizeBuf := make([]byte, LEN_SIZE)
		positionBuf := make([]byte, INDEX_SIZE)

		if len(payload)+len(sizeBuf)+len(msgBytes)+len(positionBuf)+MAX_ATTESTATION_QUOTE_SIZE > int(maxSize) {
			break
		}
		binary.BigEndian.PutUint64(sizeBuf, uint64(len(msgBytes)))
		binary.BigEndian.PutUint64(positionBuf, uint64(p))

		// Add the submitted txn position and the size of the message along with the message
		payload = append(payload, positionBuf...)
		payload = append(payload, sizeBuf...)
		payload = append(payload, msgBytes...)
		msgCnt += 1
	}
	return payload, msgCnt
}

func SignHotShotPayload(
	unsigned []byte,
	signer func([]byte) ([]byte, error),
) ([]byte, error) {
	quote, err := signer(unsigned)
	if err != nil {
		return nil, err
	}

	quoteSizeBuf := make([]byte, LEN_SIZE)
	binary.BigEndian.PutUint64(quoteSizeBuf, uint64(len(quote)))
	// Put the signature first. That would help easier parsing.
	result := quoteSizeBuf
	result = append(result, quote...)
	result = append(result, unsigned...)

	return result, nil
}

func ParseHotShotPayload(payload []byte) (signature []byte, userDataHash []byte, indices []uint64, messages [][]byte, err error) {
	if len(payload) < LEN_SIZE {
		return nil, nil, nil, nil, errors.New("payload too short to parse signature size")
	}

	// Extract the signature size
	signatureSize, err := safecast.ToInt(binary.BigEndian.Uint64(payload[:LEN_SIZE]))
	if err != nil {
		return nil, nil, nil, nil, errors.New("could not convert signature size to int")
	}

	currentPos := LEN_SIZE

	if len(payload[currentPos:]) < signatureSize {
		return nil, nil, nil, nil, errors.New("payload too short for signature")
	}

	// Extract the signature
	signature = payload[currentPos : currentPos+signatureSize]
	currentPos += signatureSize

	indices = []uint64{}
	messages = [][]byte{}

	// Take keccak256 hash of the rest of payload
	userDataHash = crypto.Keccak256(payload[currentPos:])
	// Parse messages
	for currentPos < len(payload) {

		if len(payload[currentPos:]) < LEN_SIZE+INDEX_SIZE {
			return nil, nil, nil, nil, errors.New("remaining bytes")
		}

		// Extract the index
		index := binary.BigEndian.Uint64(payload[currentPos : currentPos+INDEX_SIZE])
		currentPos += INDEX_SIZE

		// Extract the message size
		messageSize, err := safecast.ToInt(binary.BigEndian.Uint64(payload[currentPos : currentPos+LEN_SIZE]))
		if err != nil {
			return nil, nil, nil, nil, errors.New("could not convert message size to int")
		}
		currentPos += LEN_SIZE

		if len(payload[currentPos:]) < messageSize {
			return nil, nil, nil, nil, errors.New("message size mismatch")
		}

		// Extract the message
		message := payload[currentPos : currentPos+messageSize]
		currentPos += messageSize
		if len(message) == 0 {
			// If the message has a size of 0, skip adding it to the list.
			continue
		}

		indices = append(indices, index)
		messages = append(messages, message)
	}

	return signature, userDataHash, indices, messages, nil
}

// FilterAndFind filters an array in-place and returns the matching element based on a comparison function.
// The comparison function should return:
//   - FilterAndFindTarget for the element to be returned, will be kept in the array
//   - FilterAndFindKeep for elements to be kept
//   - FilterAndFindRemove for elements to be removed
//
// Returns the index of the found element (if any)
func FilterAndFind[T any](arr *[]T, compareFunc func(T) int) int {

	var hasFound bool
	idx := -1

	if arr == nil || len(*arr) == 0 {
		return idx
	}

	// `j` is the next legal index to insert an element
	j := 0
	for i := 0; i < len(*arr); i++ {
		result := compareFunc((*arr)[i])

		if result == FilterAndFind_Remove || (result == FilterAndFind_Target && hasFound) {
			// here we skip the element and do not increment `j`
			continue
		}

		// Take the first element that matches
		if result == FilterAndFind_Target {
			hasFound = true
			idx = j
		}
		if i != j {
			// current element should be kept, so we move it to the next legal index `j`.
			(*arr)[j] = (*arr)[i]
		}
		j++
	}

	// now `j` is the length of elements to keep, we truncate the array to the new length
	*arr = (*arr)[:j]
	return idx
}

// CountUniqueEntries iterates over an array with potential duplicate values and counts the unique entries.
// returns a Uint that represents the number of unique entries.
// @Dev:
func CountUniqueEntries[T any](arr *[]T) uint64 {
	var uniqueCount uint64 // Declare the variable before assignment so the compiler doesn't infer it as an int.
	entriesMap := make(map[any]bool)
	uniqueCount = 0
	for _, entry := range *arr {
		if !entriesMap[entry] {
			uniqueCount += 1
			entriesMap[entry] = true
		}

	}
	return uniqueCount
}
