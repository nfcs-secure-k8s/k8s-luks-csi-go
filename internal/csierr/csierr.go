package csierr

import (
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sanitised struct {
	st      *status.Status
	wrapped error
}

func (e *sanitised) Error() string              { return e.st.Err().Error() }
func (e *sanitised) Unwrap() error              { return e.wrapped }
func (e *sanitised) GRPCStatus() *status.Status { return e.st }

// Wrap converts an internal error into a gRPC status carrying only `code`
// and a generic "<op> failed" message. Returns nil if err is nil.
func Wrap(code codes.Code, op string, err error) error {
	if err == nil {
		return nil
	}
	return &sanitised{
		st:      status.New(code, fmt.Sprintf("%s failed", op)),
		wrapped: err,
	}
}
