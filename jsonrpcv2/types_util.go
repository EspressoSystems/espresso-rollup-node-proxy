package jsonrpcv2

import (
	"encoding/json"
	"fmt"
)

// IDToString is a helper function that converts an ID to a string for
// logging purposes.  It handles the various types that an ID can be,
// including nil, string, json.Number, and numeric types.
func IDToString(id ID) string {
	if id == nil {
		return "null"
	}

	if cast, castOK := id.(string); castOK {
		return cast
	}

	if cast, castOK := id.(json.Number); castOK {
		return cast.String()
	}

	switch t := id.(type) {
	default:
		return "unsuported ID type"

	case float32, float64:
		return fmt.Sprintf("%f", t)

	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", t)
	}
}
