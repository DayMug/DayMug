package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
)

type MarketplaceHandler struct {
	Ops *service.MarketplaceOps
}

type marketplaceAppRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	IconURL     string `json:"icon_url"`
	DeployDir   string `json:"deploy_dir"`
}

func (h *MarketplaceHandler) List(c *gin.Context) {
	apps, err := h.Ops.List(c.Request.Context())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, apps)
}

func (h *MarketplaceHandler) Create(c *gin.Context) {
	var req marketplaceAppRequest
	if !bindJSON(c, &req) {
		return
	}
	app, err := h.Ops.Create(c.Request.Context(), service.MarketplaceAppParams{
		Name:        req.Name,
		Description: req.Description,
		URL:         req.URL,
		IconURL:     req.IconURL,
		DeployDir:   req.DeployDir,
		CreatedBy:   middleware.CurrentUserID(c),
	})
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, app)
}

func (h *MarketplaceHandler) Update(c *gin.Context) {
	var req marketplaceAppRequest
	if !bindJSON(c, &req) {
		return
	}
	app, err := h.Ops.Update(c.Request.Context(), c.Param("id"), service.MarketplaceAppParams{
		Name:        req.Name,
		Description: req.Description,
		URL:         req.URL,
		IconURL:     req.IconURL,
		DeployDir:   req.DeployDir,
	})
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, app)
}

func (h *MarketplaceHandler) Delete(c *gin.Context) {
	if err := h.Ops.Delete(c.Request.Context(), c.Param("id")); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
