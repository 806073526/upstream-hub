package connector

import (
	"errors"
	"testing"
)

func TestHTTPStatusErrorPreservesStatusCode(t *testing.T) {
	err := HTTPStatusError(522, []byte("origin connection timed out"))
	var statusErr *HTTPError
	if !errors.As(err, &statusErr) {
		t.Fatalf("HTTPStatusError() error type = %T, want *HTTPError", err)
	}
	if statusErr.StatusCode != 522 {
		t.Fatalf("status code = %d, want 522", statusErr.StatusCode)
	}
	if statusErr.Error() != "status 522: origin connection timed out" {
		t.Fatalf("error = %q", statusErr.Error())
	}
}
