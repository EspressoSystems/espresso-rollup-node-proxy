package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	espressoStore "proxy/store"

	"github.com/ethereum/go-ethereum/log"
)

// JSONRPCRequest represents a JSON-RPC 2.0 request
// https://www.jsonrpc.org/specification#request_object
type JSONRPCRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object
// https://www.jsonrpc.org/specification#error_object
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type JSONRPCResponse struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// Interceptor is responsible for intercepting JSON-RPC requests with
// the specified espresso tag and replacing the tag with a block number
// finalized by Espresso. Note: the espreso tag can be "finalized", "espresso" etc
// and is configurable
const maxJSONDepth = 32

var errMaxJSONDepthExceeded = errors.New("JSON nesting depth exceeds limit")

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

// Intercept takes in a raw JSON-RPC request (single or batch), checks if the params
// contain the espresso tag and if so replaces it with the block number from the store.
// It distinguishes batch from single requests by checking if the first non-whitespace
// byte is '[' (0x5B). Since JSON-RPC payloads are UTF-8 encoded, the raw byte value
// is equivalent to the ASCII character literal.
func (i *Interceptor) Intercept(rawRequest []byte) ([]byte, error) {
	trimmed := bytes.TrimLeft(rawRequest, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return i.interceptBatch(rawRequest)
	}
	result, _, err := i.interceptSingle(rawRequest)
	return result, err
}

func (i *Interceptor) interceptBatch(rawRequest []byte) ([]byte, error) {
	var batch []json.RawMessage
	if err := json.Unmarshal(rawRequest, &batch); err != nil {
		return nil, fmt.Errorf("failed to parse batch JSON-RPC request: %w", err)
	}
	if i.maxBatchSize > 0 && len(batch) > i.maxBatchSize {
		return nil, &BatchTooLargeError{Count: len(batch), Limit: i.maxBatchSize}
	}

	state := i.store.GetState()
	if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
		log.Warn("espresso state is empty, sending rawRequest to the full node")
		return rawRequest, nil
	}

	changed := false
	// handles the case like this where we have two JSON RPC requests in the body
	// 	 `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xabc","espresso"]},
	// 	  {"jsonrpc":"2.0","id":2,"method":"eth_getBlockByNumber","params":["espresso",true]}]`
	for idx, raw := range batch {
		result, singleChanged, err := i.replaceEspressoTag(raw, state.L2BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to intercept batch element %d: %w", idx, err)
		}
		if singleChanged {
			batch[idx] = result
			changed = true
		}
	}

	if !changed {
		return rawRequest, nil
	}

	modifiedBatch, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal modified batch request: %w", err)
	}
	return modifiedBatch, nil
}

func (i *Interceptor) interceptSingle(rawRequest []byte) ([]byte, bool, error) {
	state := i.store.GetState()
	if state.FallbackHotshotHeight == 0 || state.L2BlockNumber == 0 || state.UpdatedAt.IsZero() {
		log.Warn("espresso state is empty, sending rawRequest to the full node")
		return rawRequest, false, nil
	}

	return i.replaceEspressoTag(rawRequest, state.L2BlockNumber)
}

// replaceEspressoTag is a pure state transition function that takes a raw JSON-RPC
// request and replaces occurrences of the espresso tag in its params with the given
// block number. It returns the modified request and whether any replacement was made.
func (i *Interceptor) replaceEspressoTag(rawRequest []byte, blockNumber uint64) ([]byte, bool, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(rawRequest, &req); err != nil {
		return nil, false, fmt.Errorf("failed to parse JSON-RPC request: %w", err)
	}

	log.Debug("rpc request", "rpc_method", req.Method, "rpc_id", string(req.ID))

	// If the request has no params, there is nothing to replace
	if req.Params == nil {
		return rawRequest, false, nil
	}

	replacedParams, changed, err := i.replaceTagInParams(req.Params, blockNumber, 0)
	if err != nil {
		return nil, false, fmt.Errorf("failed to replace espresso tag with block number: %w", err)
	}
	// If changed is false, this means the params didnt contain the espresso tag
	// so we return the original request without error
	if !changed {
		return rawRequest, false, nil
	}

	// Otherwise we replace the params in the request with the replaced params
	req.Params = replacedParams
	// Marshal the modified request back to json and return that
	modifiedRequest, err := json.Marshal(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal modified request: %w", err)
	}
	return modifiedRequest, true, nil
}

// replaceTagInParams recursively walks JSON params and replaces
// exact matches of the espresso tag with a hex block number.
func (i *Interceptor) replaceTagInParams(params json.RawMessage, espressoFinalizedBlockNumber uint64, depth int) (json.RawMessage, bool, error) {
	if depth > maxJSONDepth {
		return nil, false, errMaxJSONDepthExceeded
	}

	var s string

	// Case 1: params is a string containing the espresso tag
	// {"jsonrpc":"2.0","method":"eth_getBalance","params":["0xAddr","espresso"]}`
	// This case is the end of the recursion since we have found the espresso tag
	// and replaced it with the block number
	if err := json.Unmarshal(params, &s); err == nil {
		if s == i.espressoTag {
			// convert block number to hex
			blockNumberHex := fmt.Sprintf("0x%x", espressoFinalizedBlockNumber)
			replacedParams, err := json.Marshal(blockNumberHex)
			if err != nil {
				return nil, false, fmt.Errorf("failed to marshal replaced params: %w", err)
			}
			log.Debug("replaced espresso tag in params with block number", "originalParams", s, "replacedParams", replacedParams)
			return replacedParams, true, nil
		}
		return params, false, nil
	}

	// Case 2: params is a JSON object — recurse into each value
	// 	`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":{"to":"0xabc","data":"0x123","blockTag":"espresso"}}`
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err == nil {
		changed := false
		for key, value := range obj {
			result, c, err := i.replaceTagInParams(value, espressoFinalizedBlockNumber, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in object: %w", err)
			}
			if c {
				obj[key] = result
				changed = true
			}
		}
		// If changed if false, we return the original params`
		if !changed {
			return params, false, nil
		}
		// Otherwise the espresso tag was replaced
		// so we create the replaced params and return that
		replacedParams, err := json.Marshal(obj)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal replaced params: %w", err)
		}
		log.Debug("replaced espresso tag in params with block number", "originalParams", string(params), "replacedParams", string(replacedParams))
		return replacedParams, true, nil
	}

	// Case 3: params is a JSON array — recurse into each element
	// {"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["espresso",false]}
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err == nil {
		changed := false
		for j, value := range arr {
			result, c, err := i.replaceTagInParams(value, espressoFinalizedBlockNumber, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in array: %w", err)
			}
			if c {
				arr[j] = result
				changed = true
			}
		}
		// If changed if false, we return the original params
		if !changed {
			return params, false, nil
		}
		// Otherwise the espresso tag was replaced
		// so we create the replaced params and return that
		replacedParams, err := json.Marshal(arr)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal replaced params: %w", err)
		}
		log.Debug("replaced espresso tag in params with block number", "originalParams", string(params), "replacedParams", string(replacedParams))
		return replacedParams, true, nil
	}

	// If params is some other JSON primitive (number, boolean, null),
	// it cannot contain the espresso tag so return unchanged without error
	return params, false, nil
}
