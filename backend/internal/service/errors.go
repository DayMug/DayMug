package service

import (
	"errors"
	"net/http"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// ServiceError is how a use-case in this package reports a failure that the
// caller must turn into a specific HTTP status. The service layer never sees
// a *gin.Context — it returns one of these instead, and the transport layer
// (handler.respondServiceError) does the single translation into JSON.
//
// Msg is the client-facing text and is reproduced verbatim in the response
// body; Err is the underlying cause kept for logs and errors.Is/errors.As
// chains. Keeping the two apart is deliberate: several endpoints historically
// surface a bare store error message to the client, and others deliberately
// hide it behind a generic phrase. Callers pick per site rather than having
// the mapping guess.
type ServiceError struct {
	Status int
	Msg    string
	Err    error
}

// Error joins the client-facing message with the cause so a logged
// ServiceError keeps both halves. Handlers must render Message(), not this.
func (e *ServiceError) Error() string {
	if e == nil {
		return ""
	}
	switch {
	case e.Msg == "" && e.Err == nil:
		return http.StatusText(e.Status)
	case e.Msg == "":
		return e.Err.Error()
	case e.Err == nil:
		return e.Msg
	default:
		return e.Msg + ": " + e.Err.Error()
	}
}

// Unwrap exposes the cause so errors.Is/errors.As keep working through a
// ServiceError — e.g. errors.Is(err, store.ErrNotFound) still answers
// truthfully after a store failure has been wrapped.
func (e *ServiceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Message is the response body text: Msg when set, otherwise a best-effort
// rendering of the cause. Never includes the cause when Msg is present, so an
// endpoint that hides internal detail keeps hiding it.
func (e *ServiceError) Message() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return e.Msg
	}
	return e.Error()
}

// NewServiceError is the general constructor; err may be nil.
func NewServiceError(status int, msg string, err error) *ServiceError {
	return &ServiceError{Status: status, Msg: msg, Err: err}
}

// BadRequest reports caller-supplied input the use-case refuses (400).
func BadRequest(msg string) *ServiceError {
	return &ServiceError{Status: http.StatusBadRequest, Msg: msg}
}

// Forbidden reports an ownership/authorisation failure (403).
func Forbidden(msg string) *ServiceError {
	return &ServiceError{Status: http.StatusForbidden, Msg: msg}
}

// NotFound reports a missing resource (404).
func NotFound(msg string) *ServiceError {
	return &ServiceError{Status: http.StatusNotFound, Msg: msg}
}

// Conflict reports a state clash such as a job already in flight (409).
func Conflict(msg string) *ServiceError {
	return &ServiceError{Status: http.StatusConflict, Msg: msg}
}

// Internal reports a server-side failure (500). err is kept for logs.
func Internal(msg string, err error) *ServiceError {
	return &ServiceError{Status: http.StatusInternalServerError, Msg: msg, Err: err}
}

// BadGateway reports an upstream (agent CLI) failure (502).
func BadGateway(msg string, err error) *ServiceError {
	return &ServiceError{Status: http.StatusBadGateway, Msg: msg, Err: err}
}

// StoreError mirrors handler.respondStoreError inside the service layer: a
// missing row becomes 404 with the caller's phrasing, anything else becomes
// 500 carrying the raw store message. Use it wherever a handler previously
// called respondStoreError directly so the response body is unchanged.
func StoreError(err error, notFoundMsg string) *ServiceError {
	if errors.Is(err, store.ErrNotFound) {
		return &ServiceError{Status: http.StatusNotFound, Msg: notFoundMsg, Err: err}
	}
	return &ServiceError{Status: http.StatusInternalServerError, Msg: err.Error(), Err: err}
}
