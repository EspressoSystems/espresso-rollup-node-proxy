package jsonrpcv2

import (
	"fmt"
)

// Error implements error
func (e Error) Error() string {
	return fmt.Sprintf("jsonrpcv2 error, code %d, message: %s", e.Code, e.Message)
}

// Is implements a check for errors.Is, in order to ensure that the
// type matches as expected
// type matches as expected
func (e Error) Is(err error) bool {
	_, castOK := err.(Error)
	return castOK
}
