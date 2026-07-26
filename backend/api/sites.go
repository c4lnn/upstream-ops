package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bejix/upstream-ops/backend/monitor"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerSites(g *gin.RouterGroup, d *Deps) {
	gp := g.Group("/sites")
	gp.GET("", func(c *gin.Context) { listSites(c, d) })
	gp.POST("", func(c *gin.Context) { createSite(c, d) })
	gp.GET("/:site_id", func(c *gin.Context) { getSite(c, d) })
	gp.PUT("/:site_id", func(c *gin.Context) { updateSite(c, d) })
	gp.DELETE("/:site_id", func(c *gin.Context) { deleteSite(c, d) })
	gp.POST("/:site_id/sync", func(c *gin.Context) { syncSite(c, d) })
	gp.POST("/:site_id/accounts", func(c *gin.Context) { createSiteAccount(c, d) })
	gp.POST("/:site_id/default-account", func(c *gin.Context) { setDefaultSiteAccount(c, d) })
}

type siteSyncService interface {
	SyncSite(ctx context.Context, siteID uint) ([]monitor.SiteAccountSyncResult, error)
}

type siteInput struct {
	Name                string               `json:"name" binding:"required"`
	Type                storage.UpstreamType `json:"type" binding:"required"`
	BaseURL             string               `json:"base_url" binding:"required"`
	SortOrder           int                  `json:"sort_order"`
	IgnoreAnnouncements bool                 `json:"ignore_announcements"`
}

type siteUpdateInput struct {
	Name                 *string               `json:"name"`
	Type                 *storage.UpstreamType `json:"type"`
	BaseURL              *string               `json:"base_url"`
	SortOrder            *int                  `json:"sort_order"`
	IgnoreAnnouncements  *bool                 `json:"ignore_announcements"`
	ConfirmBaseURLChange bool                  `json:"confirm_base_url_change"`
}

type defaultAccountInput struct {
	AccountID uint `json:"account_id" binding:"required"`
}

type siteUpdateOutput struct {
	storage.UpstreamSite
	AffectedAccountCount int `json:"affected_account_count,omitempty"`
}

func requireSites(c *gin.Context, d *Deps) *storage.UpstreamSites {
	if d.Sites == nil {
		fail(c, http.StatusServiceUnavailable, errors.New("站点服务未配置"))
		return nil
	}
	return d.Sites
}

func listSites(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	list, err := sites.ListSummaries()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func getSite(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	summary, err := sites.Summary(siteID)
	if err != nil {
		fail(c, statusForStorageError(err), err)
		return
	}
	type siteDetail struct {
		storage.UpstreamSiteSummary
		Accounts []accountOutput `json:"accounts"`
	}
	out := siteDetail{UpstreamSiteSummary: *summary, Accounts: accountOutputs(d, summary.Accounts)}
	out.UpstreamSiteSummary.Accounts = nil
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func createSite(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	var in siteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	site := &storage.UpstreamSite{
		Name:                in.Name,
		Type:                in.Type,
		BaseURL:             in.BaseURL,
		SortOrder:           in.SortOrder,
		IgnoreAnnouncements: in.IgnoreAnnouncements,
	}
	if err := sites.Create(site); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": site})
}

func updateSite(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in siteUpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	site, err := sites.FindByID(siteID)
	if err != nil {
		fail(c, statusForStorageError(err), err)
		return
	}

	affectedAccountCount := 0
	if in.BaseURL != nil {
		normalized, normalizeErr := storage.NormalizeBaseURL(*in.BaseURL)
		if normalizeErr != nil {
			fail(c, http.StatusBadRequest, normalizeErr)
			return
		}
		if normalized != site.BaseURL {
			affectedAccountCount, err = sites.UpdateBaseURL(siteID, normalized, in.ConfirmBaseURLChange)
			if err != nil {
				fail(c, http.StatusBadRequest, err)
				return
			}
			site.BaseURL = normalized
		}
	}
	if in.Name != nil {
		site.Name = strings.TrimSpace(*in.Name)
	}
	if in.Type != nil {
		site.Type = *in.Type
	}
	if in.SortOrder != nil {
		site.SortOrder = *in.SortOrder
	}
	if in.IgnoreAnnouncements != nil {
		site.IgnoreAnnouncements = *in.IgnoreAnnouncements
	}
	if err := sites.Update(site); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": siteUpdateOutput{
		UpstreamSite:         *site,
		AffectedAccountCount: affectedAccountCount,
	}})
}

func deleteSite(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	cascade, err := strconv.ParseBool(c.DefaultQuery("cascade", "false"))
	if err != nil {
		fail(c, http.StatusBadRequest, errors.New("cascade 必须是布尔值"))
		return
	}
	if err := sites.Delete(siteID, cascade); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func syncSite(c *gin.Context, d *Deps) {
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	service, ok := d.Monitor.(siteSyncService)
	if !ok {
		fail(c, http.StatusServiceUnavailable, errors.New("站点批量同步未配置"))
		return
	}
	results, syncErr := service.SyncSite(c.Request.Context(), siteID)
	response := gin.H{"items": results, "partial": syncErr != nil}
	if syncErr != nil {
		response["error"] = syncErr.Error()
	}
	c.JSON(http.StatusOK, gin.H{"data": response})
}

func createSiteAccount(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if _, err := sites.FindByID(siteID); err != nil {
		fail(c, statusForStorageError(err), err)
		return
	}
	var in accountInput
	if err := bindAccountJSON(c, &in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	created, err := createAccountForSite(d, siteID, in)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accountOutputFor(d, *created)})
}

func setDefaultSiteAccount(c *gin.Context, d *Deps) {
	sites := requireSites(c, d)
	if sites == nil {
		return
	}
	siteID, err := uintParam(c, "site_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in defaultAccountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if err := sites.SetDefaultAccount(siteID, in.AccountID); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func statusForStorageError(err error) int {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
