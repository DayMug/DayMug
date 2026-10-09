package handler

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// DBPinger is the slice of the store the health check needs. Stores that
// cannot be pinged (test fakes) are reported healthy.
type DBPinger interface {
	Ping(ctx context.Context) error
}

const healthDBTimeout = 2 * time.Second

// NewHealthCheck answers /api/health. The upgrade watchdog treats a 200 as
// "the new release works" and otherwise rolls back, so a server whose
// database cannot be read must not report ok just because the HTTP listener
// is up.
func NewHealthCheck(db any) gin.HandlerFunc {
	pinger, _ := db.(DBPinger)
	return func(c *gin.Context) {
		if pinger != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), healthDBTimeout)
			err := pinger.Ping(ctx)
			cancel()
			if err != nil {
				log.Printf("[health] database check failed: %v", err)
				c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "database": "error"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
	}
}
