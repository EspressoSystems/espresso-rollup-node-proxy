package interceptor

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/log"
)

const ESPRESSO_TAG = "espresso"

// JSONRPCRequest represents a JSON-RPC 2.0 request
// https://www.jsonrpc.org/specification#request_object
type RPCRequest struct {
	Version string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type BlockNumberProvider interface {
	GetBlockNumber() uint64
}

type Interceptor struct {
	blockNumberProvider BlockNumberProvider
}

// Intercept takes a raw JSON-RPC request as input, parses it,
// replaces any occurrence of the espresso tag in the params with the block number,
// and returns the modified request as a byte slice.
// If any error occurs during parsing or modification,
// it returns the original raw request along with the error.
func (i *Interceptor) Intercept(rawRequest []byte) ([]byte, error) {
	var req RPCRequest
	if err := json.Unmarshal(rawRequest, &req); err != nil {
		return nil, fmt.Errorf("failed to parse JSON-RPC request: %w", err)
	}

	// If params is nil, this means it doesnt
	// contain espresso tag return without error
	if req.Params == nil {
		return rawRequest, nil
	}

	blockNumber := i.blockNumberProvider.GetBlockNumber()
	params, replaced, err := replaceEspressoTag(req.Params, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to replace espresso tag: %w", err)
	}

	if !replaced {
		return rawRequest, nil
	}

	req.Params = params

	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal modified JSON-RPC request: %w", err)
	}
	return out, nil
}

func NewInterceptor(provider BlockNumberProvider) *Interceptor {
	return &Interceptor{blockNumberProvider: provider}
}

// replaceEspressoTag recursively traverses the JSON structure to find and replace
// any occurrence of the espresso tag with the block number
// If any error occurs during unmarshalling or marshalling,
// it logs the error and returns the original raw JSON
func replaceEspressoTag(raw json.RawMessage, blockNumber uint64) (json.RawMessage, bool, error) {
	var s string

	if err := json.Unmarshal(raw, &s); err == nil {
		if s == ESPRESSO_TAG {
			blockNumHex := fmt.Sprintf("0x%x", blockNumber)
			replaced, err := json.Marshal(blockNumHex)
			if err != nil {
				return raw, false, fmt.Errorf("failed to marshal espresso tag: %w", err)
			}
			log.Debug("replaced espresso tag with block number", "blockNumber", blockNumHex)
			return replaced, true, nil
		}
		return raw, false, nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		changed := false
		for key, value := range object {
			result, c, err := replaceEspressoTag(value, blockNumber)
			if err != nil {
				return raw, false, fmt.Errorf("failed to replace espresso tag in object: %w", err)
			}
			if c {
				object[key] = result
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(object)
		if err != nil {
			return raw, false, fmt.Errorf("failed to marshal object after replacing espresso tag: %w", err)
		}
		return out, true, nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		changed := false
		for i, item := range arr {
			result, c, err := replaceEspressoTag(item, blockNumber)
			if err != nil {
				return raw, false, fmt.Errorf("failed to replace espresso tag in array: %w", err)
			}
			if c {
				arr[i] = result
				changed = true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(arr)
		if err != nil {
			return raw, false, fmt.Errorf("failed to marshal array after replacing espresso tag: %w", err)
		}
		return out, true, nil
	}

	return raw, false, nil
}
