package proxy

import (
	"bytes"
	"encoding/json"
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

// Interceptor is responsible for intercepting JSON-RPC requests with
// the specified espresso tag and replacing the tag with a block number
// finalized by Espresso. Note: the espreso tag can be "finalized", "espresso" etc
// and is configurable
type Interceptor struct {
	store       *espressoStore.EspressoStore
	espressoTag string
}

func NewInterceptor(store *espressoStore.EspressoStore, espressoTag string) *Interceptor {
	return &Interceptor{
		store:       store,
		espressoTag: espressoTag,
	}
}

// Intercept takes in a raw JSON-RPC request (single or batch), checks if the params
// contain the espresso tag and if so replaces it with the block number from the store.
func (i *Interceptor) Intercept(rawRequest []byte) ([]byte, error) {
	trimmed := bytes.TrimLeft(rawRequest, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return i.interceptBatch(rawRequest)
	}
	return i.interceptSingle(rawRequest)
}

// InterceptBatch is able to handle batch JSON RPC requests
// https://www.jsonrpc.org/specification#batch
func (i *Interceptor) interceptBatch(rawRequest []byte) ([]byte, error) {
	var batch []json.RawMessage
	if err := json.Unmarshal(rawRequest, &batch); err != nil {
		return nil, fmt.Errorf("failed to parse batch JSON-RPC request: %w", err)
	}

	changed := false
	for idx, raw := range batch {
		result, err := i.interceptSingle(raw)
		if err != nil {
			return nil, fmt.Errorf("failed to intercept batch element %d: %w", idx, err)
		}
		if !bytes.Equal(raw, result) {
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

func (i *Interceptor) interceptSingle(rawRequest []byte) ([]byte, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(rawRequest, &req); err != nil {
		return nil, fmt.Errorf("failed to parse JSON-RPC request: %w", err)
	}

	// If the request has no params, there is nothing to replace
	if req.Params == nil {
		return rawRequest, nil
	}

	state, err := i.store.GetState()
	if err != nil {
		return nil, fmt.Errorf("failed to get block number from store: %w", err)
	}
	espressoFinalizedBlockNumber := state.L2BlockNumber
	replacedParams, changed, err := i.replaceEspressoTagWithBlockNumber(req.Params, espressoFinalizedBlockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to replace espresso tag with block number: %w", err)
	}
	// If changed is false, this means the params didnt contain the espresso tag
	// so we return the original request without error
	if !changed {
		return rawRequest, nil
	}

	// Otherwise we replace the params in the request with the replaced params
	req.Params = replacedParams
	// Marshal the modified request back to json and return that
	modifiedRequest, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal modified request: %w", err)
	}
	return modifiedRequest, nil
}

// replaceEspressoTagWithBlockNumber is a recursive function that takes in the params of a JSON-RPC request and replaces any occurrence of the espresso tag with the provided block number.
// The params can be a string, json object or json array.
// The function returns the modified params, a boolean indicating whether the replacement was done and an error if any.
func (i *Interceptor) replaceEspressoTagWithBlockNumber(params json.RawMessage, espressoFinalizedBlockNumber uint64) (json.RawMessage, bool, error) {
	var s string

	// First case is where params is a string containing the espresso tag
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

	// Second case is where params is a json object in which one key value pair has
	// value as the espresso tag
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err == nil {
		changed := false
		for key, value := range obj {
			result, c, err := i.replaceEspressoTagWithBlockNumber(value, espressoFinalizedBlockNumber)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in object:%w", err)
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

	// Third case where params is a json array in which one of the values is the espresso tag
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err == nil {
		changed := false
		for j, value := range arr {
			result, c, err := i.replaceEspressoTagWithBlockNumber(value, espressoFinalizedBlockNumber)
			if err != nil {
				return nil, false, fmt.Errorf("failed to replace espresso tag in array:%w", err)
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
