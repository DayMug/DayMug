package service

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// TestServiceErrorUnwrapsForErrorsIs is the property the transport layer leans
// on: a ServiceError may be wrapped again by a caller and both errors.As (to
// find the status) and errors.Is (to find the cause) must still work.
func TestServiceErrorUnwrapsForErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("compact: %w", StoreError(store.ErrNotFound, "conversation not found"))

	var svcErr *ServiceError
	if !errors.As(wrapped, &svcErr) {
		t.Fatal("errors.As did not find the ServiceError through an extra wrap")
	}
	if svcErr.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", svcErr.Status)
	}
	if !errors.Is(wrapped, store.ErrNotFound) {
		t.Fatal("the store cause was lost; Unwrap must expose it")
	}
}

// TestStoreErrorMirrorsRespondStoreError pins the body text the handler used to
// write inline: 404 keeps the caller's phrasing, anything else surfaces the raw
// store message with a 500.
func TestStoreErrorMirrorsRespondStoreError(t *testing.T) {
	notFound := StoreError(store.ErrNotFound, "conversation not found")
	if notFound.Status != http.StatusNotFound || notFound.Message() != "conversation not found" {
		t.Fatalf("not-found mapping = %d/%q", notFound.Status, notFound.Message())
	}

	boom := errors.New("database is locked")
	other := StoreError(boom, "conversation not found")
	if other.Status != http.StatusInternalServerError || other.Message() != "database is locked" {
		t.Fatalf("generic mapping = %d/%q", other.Status, other.Message())
	}
}

// TestServiceErrorMessageHidesCause keeps the two renderings apart: Message()
// is what reaches the client and must not leak an internal cause that the
// endpoint deliberately replaced with a generic phrase, while Error() keeps
// both halves for the log.
func TestServiceErrorMessageHidesCause(t *testing.T) {
	err := Internal("save summary failed", errors.New("disk full"))
	if err.Message() != "save summary failed" {
		t.Fatalf("Message() = %q, want the generic phrase", err.Message())
	}
	if err.Error() != "save summary failed: disk full" {
		t.Fatalf("Error() = %q, want message plus cause", err.Error())
	}
}
