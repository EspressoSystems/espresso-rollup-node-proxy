package jsonrpcv2_test

import (
	"encoding/json"
	"errors"
	"testing"

	"proxy/jsonrpcv2"

	"github.com/stretchr/testify/assert"
)

// TestRequestUnmarshalJSON is a parent test for testing the various marshaling
// behavior implementations of the JSON-RPC Request object.
func TestRequestUnmarshalJSON(t *testing.T) {
	// This should be a perfectly valid JSON RPC 2 Request, adhering to the
	// specification as defined
	t.Run("standard JSON RPC Representation", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"jsonrpc":"2.0","id":1,"method":"my_method","params":{"param1":"value1","param2":2}}`

		var request jsonrpcv2.Request
		assert.NoError(json.Unmarshal([]byte(raw), &request))

		assert.Equal(json.Number("1"), request.ID, "id should match")
		assert.Equal("my_method", request.Method, "method should match")
		assert.Equal(map[string]any{"param1": "value1", "param2": json.Number("2")}, request.Params, "params should match")
		assert.Empty(request.ExtraFields, "extra fields should be empty")
	})

	// This request will be missing the `jsonrpc` field.
	// This should result in an error
	t.Run("missing version", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"id":1,"method":"my_method","params":{"param1":"value1","params":2}}`
		var request jsonrpcv2.Request
		err := json.Unmarshal([]byte(raw), &request)
		assert.ErrorIs(err, jsonrpcv2.Error{})
		var cast jsonrpcv2.Error
		assert.True(errors.As(err, &cast), "error should be of the expected type")
		assert.Equal(jsonrpcv2.CodeParseError, cast.Code)
	})

	// This request has the `jsonrpc` field, but it does not match the expected
	// value for the protocol.  Specifically "2.0".
	t.Run("invalid version", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"jsonrpc":"2.1","id":1,"method":"my_method","params":{"param1":"value1","params":2}}`
		var request jsonrpcv2.Request
		err := json.Unmarshal([]byte(raw), &request)
		assert.ErrorIs(err, jsonrpcv2.Error{})
		var cast jsonrpcv2.Error
		assert.True(errors.As(err, &cast), "error should be of the expected type")
		assert.Equal(jsonrpcv2.CodeParseError, cast.Code)
	})

	// The test ensures that extra fields that are parsed are preserved.
	t.Run("valid version with extra fields", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"jsonrpc":"2.0","id":1,"method":"my_method","params":{"param1":"value1","param2":2},"foo":"bar"}`

		var request jsonrpcv2.Request
		assert.NoError(json.Unmarshal([]byte(raw), &request))

		assert.Equal(json.Number("1"), request.ID, "id should match")
		assert.Equal("my_method", request.Method, "method should match")
		assert.Equal(map[string]any{"param1": "value1", "param2": json.Number("2")}, request.Params, "params should match")

		assert.Len(request.ExtraFields, 1, "should have one extra field")

		assert.Equal(request.ExtraFields["foo"], "bar", "extra field should match")
	})

	// This test ensures that Notifications are supported, and are identified
	// appropriately.
	t.Run("notification decoding should work", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"jsonrpc":"2.0","method":"my_method","params":{"param1":"value1","param2":2},"foo":"bar"}`

		var request jsonrpcv2.Request
		assert.NoError(json.Unmarshal([]byte(raw), &request))

		assert.Equal(jsonrpcv2.NotificationID{}, request.ID, "id should match")
		assert.Equal("my_method", request.Method, "method should match")
		assert.Equal(map[string]any{"param1": "value1", "param2": json.Number("2")}, request.Params, "params should match")

		assert.Len(request.ExtraFields, 1, "should have one extra field")

		assert.Equal(request.ExtraFields["foo"], "bar", "extra field should match")
	})

	// This test ensures that `null` IDs are supported, and are not
	// identified as a `Notification`
	t.Run("null request ids supported", func(t *testing.T) {
		assert := assert.New(t)
		raw := `{"jsonrpc":"2.0","id":null,"method":"my_method","params":{"param1":"value1","param2":2},"foo":"bar"}`

		var request jsonrpcv2.Request
		assert.NoError(json.Unmarshal([]byte(raw), &request))

		assert.Equal(nil, request.ID, "id should match")
		assert.Equal("my_method", request.Method, "method should match")
		assert.Equal(map[string]any{"param1": "value1", "param2": json.Number("2")}, request.Params, "params should match")

		assert.Len(request.ExtraFields, 1, "should have one extra field")

		assert.Equal(request.ExtraFields["foo"], "bar", "extra field should match")
	})
}

// TestResultMarshalJSON is a parent test that tests various aspects and
// behavior of marshaling the JSON-RPC Object
func TestResultMarshalJSON(t *testing.T) {
	// This test ensures that any extra fields included within the JSON-RPC
	// Request Object are preserved and exist in the resulting encoded value
	t.Run("valid version with extra fields", func(t *testing.T) {
		assert := assert.New(t)

		request := jsonrpcv2.Request{
			ID:     json.RawMessage("1"),
			Method: "my_method",
			Params: json.RawMessage(`{"param1":"value1","param2":2}`),
			ExtraFields: map[string]any{
				"foo": "bar",
			},
		}

		encodedBytes, err := json.Marshal(request)
		assert.NoError(err, "marshaling should succeed")

		encoded := string(encodedBytes)
		assert.Contains(encoded, `"jsonrpc":"2.0"`)
		assert.Contains(encoded, `"id":1`)
		assert.Contains(encoded, `"method":"my_method"`)
		assert.Contains(encoded, `"foo":"bar"`)
		assert.Contains(encoded, `"params":{"param1":"value1","param2":2}`)
	})

	// This test ensures that the "params" field is missing if they are not
	// included in the encoding.
	t.Run("no params", func(t *testing.T) {
		assert := assert.New(t)

		request := jsonrpcv2.Request{
			ID:     json.Number("1"),
			Method: "my_method",
		}

		encodedBytes, err := json.Marshal(request)
		assert.NoError(err, "marshaling should succeed")

		encoded := string(encodedBytes)
		assert.Contains(encoded, `"jsonrpc":"2.0"`)
		assert.Contains(encoded, `"method":"my_method"`)
		assert.Contains(encoded, `"id":1`)
		assert.NotContains(encoded, `"params"`, "params should not be included when empty")
	})

	// This test ensures that not specifying an "id" still populates an "id"
	// of null in the encoded reprsentation.
	t.Run("null id", func(t *testing.T) {
		assert := assert.New(t)

		request := jsonrpcv2.Request{
			Method: "my_method",
		}

		encodedBytes, err := json.Marshal(request)
		assert.NoError(err, "marshaling should succeed")

		encoded := string(encodedBytes)
		assert.Contains(encoded, `"jsonrpc":"2.0"`)
		assert.Contains(encoded, `"method":"my_method"`)
		assert.Contains(encoded, `"id":null`)
		assert.NotContains(encoded, `"params"`, "params should not be included when empty")
	})

	// This test ensures that specifying an ID type of "NotificationID" will
	// result in no "id" field in the encoding.
	t.Run("notification id", func(t *testing.T) {
		assert := assert.New(t)

		request := jsonrpcv2.Request{
			ID:     jsonrpcv2.NotificationID{},
			Method: "my_method",
		}

		encodedBytes, err := json.Marshal(request)
		assert.NoError(err, "marshaling should succeed")

		encoded := string(encodedBytes)
		assert.Contains(encoded, `"jsonrpc":"2.0"`)
		assert.Contains(encoded, `"method":"my_method"`)
		assert.NotContains(encoded, `"id":`)
		assert.NotContains(encoded, `"params"`, "params should not be included when empty")
	})
}
