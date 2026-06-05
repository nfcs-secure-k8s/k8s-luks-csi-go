package csierr

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWrapHidesDetailButKeepsCode(t *testing.T) {
	internal := errors.New("vault 403 for token hvs.CAESIJ-secret-fragment")
	err := Wrap(codes.PermissionDenied, "fetch key", internal)

	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Wrap did not return a gRPC status error")
	}
	if st.Code() != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", st.Code())
	}
	if strings.Contains(st.Message(), "hvs.CAESIJ") ||
		strings.Contains(st.Message(), "secret-fragment") {
		t.Fatalf("status message leaked secret: %q", st.Message())
	}
}

func TestWrapNilReturnsNil(t *testing.T) {
	if err := Wrap(codes.Internal, "op", nil); err != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", err)
	}
}

func TestUnwrapExposesOriginalInternally(t *testing.T) {
	internal := errors.New("boom")
	err := Wrap(codes.Internal, "op", internal)
	if !errors.Is(err, internal) {
		t.Fatal("errors.Is could not recover the wrapped internal error")
	}
}
