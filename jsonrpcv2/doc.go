// Package jsonrpcv2 defines a JSON-RPC 2.0 compliant request, response, and
// error structure, as well as defining constants that can be utilized /
// referenced when providing support for JSON-RPC 2.0 protocols.
//
// Additionally, it extends the JSON-RPC 2.0 request objects by allowing
// for the inclusion of not explicitly supported fields to be utilized and
// maintained in the request body, response body, and error objects. This
// allows for requests with extra fields to keep their extra fields as needed
// when handling and processing requests coming in.
package jsonrpcv2
