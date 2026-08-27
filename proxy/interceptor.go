package proxy

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/EspressoSystems/espresso-rollup-node-proxy/jsonrpcv2"
	espressoStore "github.com/EspressoSystems/espresso-rollup-node-proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

const (
	DefaultMaxBatchSize       = 1000
	DefaultMaxRequestBodySize = 5 * 1024 * 1024 // 5MB, matches go-ethereum defaultBodyLimit
)

// maxJSONDepth bounds how deep replaceTagInParams recurses into request
// params before failing with ErrMaxJSONDepthExceeded.
const maxJSONDepth = 32

// ErrMaxJSONDepthExceeded is returned when the JSON nesting depth exceeds
// the defined limit.
var ErrMaxJSONDepthExceeded = errors.New("JSON nesting depth exceeds limit")

// ErrMaxBatchSizeExceeded is an error that indicates that we're over the
// limit of the maximum number of requests we'll perform in a single batch.
var ErrMaxBatchSizeExceeded = errors.New("maximum number of json requests in a single batch exceeded")

// Interceptor rewrites JSON-RPC requests so that every occurrence of a
// configured espresso tag is replaced with the L2 block number finalized by
// Espresso. Any string can be configured as a tag — a standard one such as
// "finalized" or "safe", the default "espresso", or a custom value — and all
// configured tags resolve to the same block number, encoded as a hex quantity
// (e.g. "0x64").
//
// Tags are matched by these rules, which apply identically to every
// configured tag:
//
//   - Exact, case-sensitive string equality. Tags are never matched by
//     prefix, by substring, or after trimming whitespace.
//   - Any string value at any depth: positional arrays, object values
//     (e.g. {"blockTag": "finalized"}) and nested structures. Object keys are
//     never rewritten, and neither are the request's method or id, which are
//     not part of params.
//   - Method-agnostic: the interceptor does not know which parameter of
//     which method is the block parameter, so a matching string is rewritten
//     wherever it appears.
type Interceptor interface {
	// InterceptRequest takes a JSON-RPC request, and attempts to perform an
	// intercept on it.  This means that the request may be rewritten and
	// modified to be different.
	InterceptRequest(jsonrpcv2.Request) (jsonrpcv2.Request, error)

	// InterceptBatchRequests takes in a batch of JSON-RPC requests, and
	// attempts to perform an intercept on each request in the batch.
	InterceptBatchRequests([]jsonrpcv2.Request) ([]jsonrpcv2.Request, error)
}

type interceptor struct {
	logger       log.Logger
	store        *espressoStore.EspressoStore
	espressoTags []string
	maxBatchSize int
}

var _ Interceptor = (*interceptor)(nil)

type BatchTooLargeError struct {
	Count int
	Limit int
}

func (e *BatchTooLargeError) Error() string {
	return fmt.Sprintf("batch too large (count %d exceeds limit %d)", e.Count, e.Limit)
}

func NewInterceptor(logger log.Logger, store *espressoStore.EspressoStore, espressoTags []string, maxBatchSize int) Interceptor {
	if logger == nil {
		logger = log.Root()
	}
	return &interceptor{
		logger:       logger,
		store:        store,
		espressoTags: espressoTags,
		maxBatchSize: maxBatchSize,
	}
}

// ErrUnknownEspressoFinalizedBlockNumber is a sentinel error indicating that
// our local state for the Espresso Finalized Block is invalid, and we cannot
// reliably know if the value is correct or now.
var ErrUnknownEspressoFinalizedBlockNumber = errors.New("espresso state is empty, finalized espresso block is unknown")

// getCurrentEspressoFinalizedBlockNumber is a helper function that retrieves
// the current finalized block number from the store.
func (i *interceptor) getCurrentEspressoFinalizedBlockNumber() (uint64, error) {
	state := i.store.GetState()

	// Check the current Espresso State for validity
	if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
		return 0, ErrUnknownEspressoFinalizedBlockNumber
	}

	return state.L2BlockNumber, nil
}

// hexBlockNumber encodes n as a JSON-RPC hex quantity (e.g. "0x64").
func hexBlockNumber(n uint64) string {
	return "0x" + strconv.FormatUint(n, 16)
}

// InterceptRequest takes in a JSON-RPC request, checks if the params contain
// a configured espresso tag and if so replaces it with the block number from
// the store. While the store holds no Espresso state yet, the request is
// returned unchanged.
//
// NOTE: This is a pure function, and if the parameters are modified, a new
// object will be returned instead of modifying the existing object.
func (i *interceptor) InterceptRequest(request jsonrpcv2.Request) (jsonrpcv2.Request, error) {
	finalizedEspressoBlockNumber, err := i.getCurrentEspressoFinalizedBlockNumber()
	if err != nil {
		i.logger.Warn("espresso state is empty, sending rawRequest to the full node", "err", err)
		return request, nil
	}

	return i.interceptRequest(request, hexBlockNumber(finalizedEspressoBlockNumber))
}

// InterceptBatchRequests takes in a batch of JSON-RPC requests, and performs
// any espresso tag expansion on the requests before returning them.
func (i *interceptor) InterceptBatchRequests(requests []jsonrpcv2.Request) ([]jsonrpcv2.Request, error) {
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
		i.logger.Warn("espresso state is empty, sending rawRequest to the full node", "err", err)
		return requests, nil
	}

	// The same for every request in the batch, so encode it once.
	blockHex := hexBlockNumber(finalizedEspressoBlockNumber)
	next := make([]jsonrpcv2.Request, len(requests))
	for j, req := range requests {
		r, err := i.interceptRequest(req, blockHex)
		if err != nil {
			return nil, err
		}

		next[j] = r
	}

	return next, nil
}

func (i *interceptor) interceptRequest(request jsonrpcv2.Request, espressoFinalizedBlockHex string) (jsonrpcv2.Request, error) {
	nextParams, changed, err := i.replaceTagInParams(request.Params, espressoFinalizedBlockHex, 0)
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

// replaceTagInParams recursively walks JSON params and replaces every string
// value that equals one of the configured espresso tags with
// espressoFinalizedBlockHex. See [Interceptor] for the matching rules.
func (i *interceptor) replaceTagInParams(params any, espressoFinalizedBlockHex string, depth int) (any, bool, error) {
	if depth > maxJSONDepth {
		return nil, false, errors.Join(
			ErrMaxJSONDepthExceeded,
			jsonrpcv2.Error{
				Code:    jsonrpcv2.CodeInternalError,
				Message: fmt.Sprintf("JSON nesting depth exceeds limit of %d", maxJSONDepth),
			},
		)
	}

	// Case 1: params is a string containing one of the espresso tags
	// {"jsonrpc":"2.0","method":"eth_getBalance","params":["0xAddr","espresso"]}`
	// This case is the end of the recursion since we have found an espresso tag
	// and replaced it with the block number
	if cast, castOK := params.(string); castOK && slices.Contains(i.espressoTags, cast) {
		return espressoFinalizedBlockHex, true, nil
	}

	// Case 2: params is a JSON object — recurse into each value
	// 	`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
	if cast, castOK := params.(map[string]any); castOK {
		var nextParams map[string]any
		for key, value := range cast {
			next, c, err := i.replaceTagInParams(value, espressoFinalizedBlockHex, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in object: %w", err)
			}
			if !c {
				continue
			}
			if nextParams == nil {
				nextParams = make(map[string]any, len(cast))
				for k, v := range cast {
					nextParams[k] = v
				}
			}
			nextParams[key] = next
		}
		if nextParams != nil {
			return nextParams, true, nil
		}
		return cast, false, nil
	}

	// Case 3: params is a JSON array — recurse into each element
	// {"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["espresso",false]}
	if cast, castOK := params.([]any); castOK {
		var nextParams []any
		for j, value := range cast {
			next, c, err := i.replaceTagInParams(value, espressoFinalizedBlockHex, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in array: %w", err)
			}
			if !c {
				if nextParams != nil {
					nextParams[j] = value
				}
				continue
			}
			if nextParams == nil {
				nextParams = make([]any, len(cast))
				copy(nextParams, cast[:j])
			}
			nextParams[j] = next
		}
		if nextParams != nil {
			return nextParams, true, nil
		}
		return cast, false, nil
	}

	// If params is some other JSON primitive (number, boolean, null),
	// it cannot contain the espresso tag so return unchanged without error
	return params, false, nil
}
