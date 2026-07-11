package cgrouptrack

import (
	"context"
	"errors"
	"testing"
)

func TestClientReturnsStructuredAPIError(t *testing.T) {
	err := responseError(Response{OK: false, Error: &APIError{Code: "denied", Message: "not allowed"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "denied" {
		t.Fatalf("error = %#v", err)
	}
	if responseError(Response{OK: true}) != nil {
		t.Fatal("successful response returned error")
	}
}

func TestClientRejectsInvalidRequestBeforeDial(t *testing.T) {
	_, err := NewClient("/missing").Do(context.Background(), Request{Operation: OpAttach, Mode: ModeUser, UID: 1000, Unit: "demo.service"})
	if err == nil {
		t.Fatal("invalid request was dialed")
	}
}
