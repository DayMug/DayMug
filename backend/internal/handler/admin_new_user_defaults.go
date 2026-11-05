package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

func (h *AdminHandler) GetNewUserDefaults(c *gin.Context) {
	d, err := service.LoadNewUserDefaults(c.Request.Context(), h.Store, h.Cfg)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (h *AdminHandler) PutNewUserDefaults(c *gin.Context) {
	var req service.NewUserDefaults
	if !bindJSON(c, &req) {
		return
	}
	d, err := service.SaveNewUserDefaults(c.Request.Context(), h.Store, h.Cfg, req)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}
