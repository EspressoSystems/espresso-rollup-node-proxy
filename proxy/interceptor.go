package proxy

import (
	"errors"
	"fmt"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

const (
	DefaultMaxBatchSize       = 1000
	DefaultMaxRequestBodySize = 5 * 1024 * 1024 // 5MB, matches go-ethereum defaultBodyLimit
)

// Interceptor is responsible for intercepting JSON-RPC requests with
// the specified espresso tag and replacing the tag with a block number
// finalized by Espresso. Note: the espreso tag can be "finalized", "espresso" etc
// and is configurable
const maxJSONDepth = 32

// ErrMaxJSONDepthExceeded is returned when the JSON nesting depth exceeds
// the defined limit.
var ErrMaxJSONDepthExceeded = errors.New("JSON nesting depth exceeds limit")

// ErrMaxBatchSizeExceeded is an error that indicates that we're over the
// limit of the maximum number of requests we'll perform in a single batch.
var ErrMaxBatchSizeExceeded = errors.New("maximum number of json requests in a single batch exceeded")

type Interceptor struct {
	store        *espressoStore.EspressoStore
	espressoTag  string
	maxBatchSize int
}

type BatchTooLargeError struct {
	Count int
	Limit int
}

func (e *BatchTooLargeError) Error() string {
	return fmt.Sprintf("batch too large (count %d exceeds limit %d)", e.Count, e.Limit)
}

func NewInterceptor(store *espressoStore.EspressoStore, espressoTag string, maxBatchSize int) *Interceptor {
	return &Interceptor{
		store:        store,
		espressoTag:  espressoTag,
		maxBatchSize: maxBatchSize,
	}
}

// ErrUnknownEspressoFinalizedBlockNumber is a sentinel error indicating that
// our local state for the Espresso Finalized Block is invalid, and we cannot
// reliably know if the value is correct or now.
var ErrUnknownEspressoFinalizedBlockNumber = errors.New("espresso state is empty, finalized espresso block is unknown")

// getCurrentEspressoFinalizedBlockNumber is a helper function that retrieves
// the current finalized block number from the store.
func (i *Interceptor) getCurrentEspressoFinalizedBlockNumber() (uint64, error) {
	state := i.store.GetState()

	// Check the current Espresso State for validity
	if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
		return 0, ErrUnknownEspressoFinalizedBlockNumber
	}

	return state.L2BlockNumber, nil
}

// InterceptRequest takes in a JSON-RPC request, checks if the params contain
// the espresso tag and if so replaces it with the block number from the
// store. It returns the modified request and whether any replacement
// was made.
//
// NOTE: This is a pure function, and if the parameters are modified, a new
// object will be returned instead of modifying the existing object.
func (i *Interceptor) InterceptRequest(request jsonrpcv2.Request) (jsonrpcv2.Request, error) {
	finalizedEspressoBlockNumber, err := i.getCurrentEspressoFinalizedBlockNumber()
	if err != nil {
		log.Warn("espresso state is empty, sending rawRequest to the full node", "err", err)
		return request, nil
	}

	return i.interceptRequest(request, finalizedEspressoBlockNumber)
}

// InterceptBatchRequests takes in a batch of JSON-RPC requests, and performs
// any espresso tag expansion on the requests before returning them.
func (i *Interceptor) InterceptBatchRequests(requests []jsonrpcv2.Request) ([]jsonrpcv2.Request, error) {
	if len(requests) > i.maxBatchSize {
		// We're over our limit of maximum batches to process.
		return nil, errors.Join(
			ErrMaxBatchSizeExceeded,
			jsonrpcv2.Error{
				Code:    jsonrpcv2.CodeInvalidRequest,
				Message: fmt.Sprintf("batch size %d exceeds maximum batch size of %d", len(requests), i.maxBatchSize),
			},
		)
	}

	finalizedEspressoBlockNumber, err := i.getCurrentEspressoFinalizedBlockNumber()
	if err != nil {
		log.Warn("espresso state is empty, sending rawRequest to the full node", "err", err)
		return requests, nil
	}

	next := make([]jsonrpcv2.Request, len(requests))
	for j, req := range requests {
		r, err := i.interceptRequest(req, finalizedEspressoBlockNumber)
		if err != nil {
			return requests, err
		}

		next[j] = r
	}

	return next, nil
}

func (i *Interceptor) interceptRequest(request jsonrpcv2.Request, espressoFinalizedBlockNumber uint64) (jsonrpcv2.Request, error) {
	nextParams, changed, err := i.replaceTagInParams(request.Params, espressoFinalizedBlockNumber, 0)
	if err != nil {
		return request, err
	}

	if !changed {
		return request, nil
	}

	return jsonrpcv2.Request{
		ID:          request.ID,
		Method:      request.Method,
		Params:      nextParams,
		ExtraFields: request.ExtraFields,
	}, nil
}

// replaceTagInParams recursively walks JSON params and replaces
// exact matches of the espresso tag with a hex block number.
func (i *Interceptor) replaceTagInParams(params any, espressoFinalizedBlockNumber uint64, depth int) (any, bool, error) {
	if depth > maxJSONDepth {
		return nil, false, errors.Join(
			ErrMaxJSONDepthExceeded,
			jsonrpcv2.Error{
				Code:    jsonrpcv2.CodeInternalError,
				Message: fmt.Sprintf("JSON nesting depth exceeds limit of %d", maxJSONDepth),
			},
		)
	}

	// Case 1: params is a string containing the espresso tag
	// {"jsonrpc":"2.0","method":"eth_getBalance","params":["0xAddr","espresso"]}`
	// This case is the end of the recursion since we have found the espresso tag
	// and replaced it with the block number
	if cast, castOK := params.(string); castOK && cast == i.espressoTag {
		return fmt.Sprintf("0x%x", espressoFinalizedBlockNumber), true, nil
	}

	// Case 2: params is a JSON object — recurse into each value
	// 	`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
	if cast, castOK := params.(map[string]any); castOK {
		nextParams := map[string]any{}
		var changed bool
		for key, value := range cast {
			next, c, err := i.replaceTagInParams(value, espressoFinalizedBlockNumber, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in object: %w", err)
			}

			if !c {
				nextParams[key] = value
				continue
			}

			nextParams[key] = next
			changed = true
		}

		if changed {
			return nextParams, true, nil
		}

		return cast, false, nil
	}

	// Case 3: params is a JSON array — recurse into each element
	// {"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["espresso",false]}
	if cast, castOK := params.([]any); castOK {
		var changed bool
		nextParams := make([]any, len(cast))
		for j, value := range cast {
			next, c, err := i.replaceTagInParams(value, espressoFinalizedBlockNumber, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in array: %w", err)
			}

			if !c {
				nextParams[j] = value
				continue
			}

			nextParams[j] = next
			changed = true
		}

		if changed {
			return nextParams, true, nil
		}

		return cast, false, nil
	}

	// If params is some other JSON primitive (number, boolean, null),
	// it cannot contain the espresso tag so return unchanged without error
	return params, false, nil
}
