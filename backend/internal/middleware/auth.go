package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// SessionCookieName is the HttpOnly cookie that carries the session token.
const SessionCookieName = "daymug_session"

// contextUserIDKey is the gin.Context key under which the authenticated
// user's ID is stored after successful auth middleware execution.
const contextUserIDKey = "auth_user_id"

// contextIsAdminKey is the gin.Context key for the authenticated user's
// admin flag, populated by RequireAuth and consumed by RequireAdmin.
const contextIsAdminKey = "auth_is_admin"

// CurrentUserID returns the authenticated user's ID for the current request,
// or an empty string when no auth middleware ran (or it didn't set the key).
func CurrentUserID(c *gin.Context) string {
	v, ok := c.Get(contextUserIDKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// CurrentUserIsAdmin returns whether the authenticated user has the admin
// flag set. False if no auth middleware ran.
func CurrentUserIsAdmin(c *gin.Context) bool {
	v, ok := c.Get(contextIsAdminKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// RequireAuth aborts the request with 401 unless a valid session cookie is
// present in the store and the underlying user is not disabled. On success it
// puts the user_id and is_admin flag into the request context.
func RequireAuth(s store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(SessionCookieName)
		if err != nil || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
			return
		}
		sess, err := s.GetSession(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Re-check disabled status on every request: an admin can disable an
		// active user and want them booted before their cookie expires. The
		// store hit is unavoidable here because the disabled flag isn't
		// embedded in the session row.
		user, err := s.GetUser(c.Request.Context(), sess.UserID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
			return
		}
		if user.Disabled {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "account disabled"})
			return
		}
		c.Set(contextUserIDKey, sess.UserID)
		c.Set(contextIsAdminKey, user.IsAdmin)
		c.Next()
	}
}

// RequireAdmin must run after RequireAuth. Aborts with 403 unless the
// authenticated user has the is_admin flag.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !CurrentUserIsAdmin(c) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin required"})
			return
		}
		c.Next()
	}
}
