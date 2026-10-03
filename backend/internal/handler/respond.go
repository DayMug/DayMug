package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// respondServiceError is the single translation point from a service-layer
// failure to an HTTP response. A *service.ServiceError already carries the
// status and the exact client-facing text the endpoint used to write inline,
// so the body shape is unchanged by the handler→service move.
//
// errors.As (not a type assertion) so a use-case is free to wrap its own
// context around a ServiceError later without breaking the mapping. Anything
// that isn't a ServiceError falls back to the historical store mapping —
// 404/500 — which is what every pre-refactor call site did with a bare error.
func respondServiceError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		if svcErr.Status == http.StatusInternalServerError {
			respondInternalErrorMessage(c, "service", err, internalServiceMessage(svcErr))
			return
		}
		c.JSON(svcErr.Status, gin.H{"error": svcErr.Message()})
		return
	}
	respondStoreError(c, err, "not found")
}

// internalErrorMessage is the whole client-facing body of an unexpected 500.
// The underlying error text (SQLite messages, host paths, syscall detail) is
// for the operator, so it goes to the log and never onto the wire.
const internalErrorMessage = "internal error"

// respondInternalError logs err with the route and a caller label, then writes
// a generic 500. Use it wherever a handler would otherwise echo err.Error()
// into a 500 body; internal_error_guard_test.go enforces that.
func respondInternalError(c *gin.Context, label string, err error) {
	respondInternalErrorMessage(c, label, err, internalErrorMessage)
}

// respondInternalErrorMessage is respondInternalError with a fixed, safe
// client message, for the few 500s whose text the UI shows as-is and that are
// more useful to the operator than "internal error" (e.g. which step of a
// rollback failed). msg must never be derived from err.
func respondInternalErrorMessage(c *gin.Context, label string, err error, msg string) {
	log.Printf("[%s] %s %s: %v", label, c.Request.Method, c.FullPath(), err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": msg})
}

// internalServiceMessage keeps a use-case's hand-written 500 text ("save
// summary failed") but hides one that is really the cause in disguise: many
// service paths still build Internal(err.Error(), err), and a message that
// embeds the wrapped error's text is exactly the leak respondInternalError
// exists to stop.
func internalServiceMessage(svcErr *service.ServiceError) string {
	msg := svcErr.Msg
	if msg == "" {
		return internalErrorMessage
	}
	if svcErr.Err != nil {
		if cause := svcErr.Err.Error(); cause != "" && strings.Contains(msg, cause) {
			return internalErrorMessage
		}
	}
	return msg
}
