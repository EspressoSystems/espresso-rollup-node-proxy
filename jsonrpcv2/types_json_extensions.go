package jsonrpcv2

import (
	"bytes"
	"encoding/json"
)

// fullDecode is a helper struct used to decode both the known and unknown
// fields of a JSON object in a single unmarshal step.
//
// This exists as a convenience placholder so we don't have to perform a
// double decode with multiple json.Decoders for each type we implement
// json.Unmarshaler for.
type fullDecode[T any] struct {
	Known T
	All   ExtraFields
}

// UnmarshalJSON implements the json.Unmarshaler
//
// This unmarshaling step will decode the given data byte slice twice. Once
// into a well-known structure, and the second into a catch-all structure.
//
// NOTE: The resulting `All` field **will** contain duplicate data for the
// `Known` structure. The data will need to be manually filtered to prune
// out duplicates, as that does not happen in this step.
//
// NOTE: This unmarshaling step utilizes a `json.Decoder` with a preference
// to utilize `json.Number` instead of the raw numeric values, which would
// default to `float64`. Manual adjustment may be needed.
func (r *fullDecode[T]) UnmarshalJSON(data []byte) error {
	var decoded fullDecode[T]

	{
		// Decode the Known Part
		dec := json.NewDecoder(bytes.NewBuffer(data))
		dec.UseNumber()

		if err := dec.Decode(&decoded.Known); err != nil {
			return err
		}
	}

	{
		// Decode the catch-All part
		dec := json.NewDecoder(bytes.NewBuffer(data))
		dec.UseNumber()

		if err := dec.Decode(&decoded.All); err != nil {
			return err
		}
	}

	*r = decoded
	return nil
}

// UnmarshalJSON implements the json.Unmarshaler
//
// This implementation of UnmarshalJSON is designed to support the known set
// of fields in a JSON-RPC request, as well as any additional fields that
// may exist within the request.
func (r *Request) UnmarshalJSON(data []byte) error {
	type toDecode struct {
		Version string `json:"jsonrpc"`
		ID      ID     `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  Params `json:"params,omitempty"`
	}

	var full fullDecode[toDecode]

	if err := json.Unmarshal(data, &full); err != nil {
		return err
	}

	if full.Known.Version != Version {
		return invalidJSONRPCVersion(full.Known.Version)
	}

	if _, idExists := full.All["id"]; !idExists && full.Known.ID == nil {
		// This is a special case where the "id" field is missing entirely
		// from the request.
		//
		// As a result the full.Known.ID is `nil`, but it's really meant to be
		// unset entirely.  So we'll replace the known ID with the NotificationID
		// in order to signify that this Request is actually a Notification
		full.Known.ID = NotificationID{}
	}

	// Delete the well known fields, as they're already accounted for.
	delete(full.All, "jsonrpc")
	delete(full.All, "id")
	delete(full.All, "method")
	delete(full.All, "params")

	*r = Request{
		ID:          full.Known.ID,
		Method:      full.Known.Method,
		Params:      full.Known.Params,
		ExtraFields: full.All,
	}

	return nil
}

// MarshalJSON implements the json.Marshaler
//
// This implementation of MarshalJSON supports preserving any extra fields
// that may exist within the request.
func (r Request) MarshalJSON() ([]byte, error) {
	toEncode := map[string]any{}
	for k, v := range r.ExtraFields {
		if k == "id" || k == "params" {
			continue
		}

		toEncode[k] = v
	}

	if r.ID != (NotificationID{}) {
		toEncode["id"] = r.ID
	}
	toEncode["method"] = r.Method
	if r.Params != nil {
		toEncode["params"] = r.Params
	}
	toEncode["jsonrpc"] = Version

	return json.Marshal(toEncode)
}

// MarshalJSON implements the json.Marshaler
//
// This implementation of MarshalJSON supports preserving any extra fields
// that max exist within the Error.
func (e Error) MarshalJSON() ([]byte, error) {
	toEncode := map[string]any{}

	for k, v := range e.ExtraFields {
		if k == "data" {
			continue
		}

		toEncode[k] = v
	}

	toEncode["code"] = e.Code
	toEncode["message"] = e.Message
	if e.Data != nil {
		toEncode["data"] = e.Data
	}

	return json.Marshal(toEncode)
}

// UnmarshalJSON implements the json.Unmarshaler
//
// This implementation of UnmarshalJSON is designed to support the known set
// of fields that exist on the Error object, as well as any extra fields
// that may be included.
func (e *Error) UnmarshalJSON(data []byte) error {
	type toDecode struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data,omitempty"`
	}

	var full fullDecode[toDecode]

	if err := json.Unmarshal(data, &full); err != nil {
		return err
	}

	// Delete the well-known fields, as they're already accounted for.
	delete(full.All, "code")
	delete(full.All, "message")
	delete(full.All, "data")

	*e = Error{
		Code:        full.Known.Code,
		Message:     full.Known.Message,
		Data:        full.Known.Data,
		ExtraFields: full.All,
	}
	return nil
}

// UnmarshalJSON implements the json.Unmarshaler
//
// This implementation of UnmarshalJSON is designed to support the known set
// of fields that exist on the JSON-RPC Response object, as well as any
// additional fields that may be included within the response.
func (r *Response) UnmarshalJSON(data []byte) error {
	type toDecode struct {
		JSONRPC string `json:"jsonrpc"`
		ID      ID     `json:"id"`
		Result  any    `json:"result,omitempty"`
		Error   *Error `json:"error,omitempty"`
	}

	var full fullDecode[toDecode]

	if err := json.Unmarshal(data, &full); err != nil {
		return err
	}

	if full.Known.JSONRPC != Version {
		return invalidJSONRPCVersion(full.Known.JSONRPC)
	}

	// delete the well-known fields, as they're already accounted for.
	delete(full.All, "jsonrpc")
	delete(full.All, "id")
	delete(full.All, "result")
	delete(full.All, "error")

	*r = Response{
		ID:          full.Known.ID,
		Result:      full.Known.Result,
		Error:       full.Known.Error,
		ExtraFields: full.All,
	}
	return nil
}

// MarshalJSON implements the json.Marshaler
func (r Response) MarshalJSON() ([]byte, error) {
	toEncode := map[string]any{}

	for k, v := range r.ExtraFields {
		if k == "result" || k == "error" {
			continue
		}
		toEncode[k] = v
	}

	if r.Error != nil {
		toEncode["error"] = r.Error
	} else {
		toEncode["result"] = r.Result
	}

	// We put these at the end, just in case someone is trying to pull
	// a "fast one", and overwrite these fields with data in the ExtraFields
	// map
	toEncode["id"] = r.ID
	toEncode["jsonrpc"] = Version

	return json.Marshal(toEncode)
}

// MarshalJSON implements json.Marshaler
//
// We set this to "null" just in case we miss in in some cases.
// (Specifically in the cases where we just take the ID from the Request, and
// assign it directly to the Response object)
func (NotificationID) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}
