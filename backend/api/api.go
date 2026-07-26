// Package api 注册所有 HTTP 路由，组装各业务 handler。
//
// 单用户场景下走 HMAC token 鉴权：账号密码写在 config 里，登录后下发 token。
package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/accountbundle"
	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/runtimeconfig"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/bejix/upstream-ops/backend/syncer"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type monitorService interface {
	RefreshBalance(ctx context.Context, account *storage.UpstreamAccount) error
	RefreshRates(ctx context.Context, account *storage.UpstreamAccount) error
	CheckSubscriptionUsageAlerts(ctx context.Context, account *storage.UpstreamAccount) error
}

type accountService interface {
	Create(in account.CreateInput) (*storage.UpstreamAccount, error)
	Update(id uint, in account.UpdateInput) (*storage.UpstreamAccount, error)
	Delete(id uint) error
	ClearLoginInfo(id uint) (*storage.UpstreamAccount, error)
	TestLogin(ctx context.Context, accountID uint) error
	RedeemCode(ctx context.Context, accountID uint, code string) (*connector.RedeemResult, error)
	GetRechargeInfo(ctx context.Context, accountID uint) (*connector.RechargeInfo, error)
	CreateRecharge(ctx context.Context, accountID uint, req connector.RechargeRequest) (*connector.RechargeLaunch, error)
	GetSubscriptionInfo(ctx context.Context, accountID uint) (*connector.SubscriptionInfo, error)
	CreateSubscription(ctx context.Context, accountID uint, req connector.SubscriptionRequest) (*connector.SubscriptionLaunch, error)
	GetSubscriptionUsage(ctx context.Context, accountID uint) (*connector.SubscriptionUsageInfo, error)
	ListAPIKeys(ctx context.Context, accountID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error)
	ListAPIKeyGroups(ctx context.Context, accountID uint) ([]connector.APIKeyGroup, error)
	CreateAPIKey(ctx context.Context, accountID uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error)
	UpdateAPIKey(ctx context.Context, accountID uint, keyID int64, req connector.APIKeyUpdateRequest) (*connector.APIKey, error)
	DeleteAPIKey(ctx context.Context, accountID uint, keyID int64) error
	RevealAPIKey(ctx context.Context, accountID uint, keyID int64) (string, error)
}

type siteAccountBundleService interface {
	Export(ctx context.Context, options accountbundle.ExportOptions) ([]byte, error)
	Preview(ctx context.Context, data []byte, options accountbundle.ImportOptions) (*accountbundle.ImportPlan, error)
	Import(ctx context.Context, data []byte, options accountbundle.ImportOptions, expectedDigest string) (*accountbundle.ImportResult, error)
}

// Deps 把所有 handler 需要的依赖打包传入。
type Deps struct {
	DB             *gorm.DB
	Cipher         *crypto.Cipher
	Runtime        *runtimeconfig.Manager
	Accounts       *storage.UpstreamAccounts
	Sites          *storage.UpstreamSites
	Sessions       *storage.AuthSessions
	Captchas       *storage.Captchas
	Notifies       *storage.Notifications
	Announcements  *storage.UpstreamAnnouncements
	Rates          *storage.Rates
	MonLogs        *storage.MonitorLogs
	AccountSvc     accountService
	AccountBundles siteAccountBundleService
	Monitor        monitorService
	Dispatcher     *notify.Dispatcher
	UpstreamSync   *syncer.Service
	Log            *slog.Logger

	// Frontend 可选：传入嵌入的前端 dist 文件系统。nil 表示不挂载（本地开发用 vite dev server）。
	Frontend fs.FS
}

// Register 把所有路由挂到给定 gin engine。
func Register(r *gin.Engine, d *Deps) {
	r.GET("/healthz", func(c *gin.Context) {
		sqlDB, err := d.DB.DB()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "down", "err": err.Error()})
			return
		}
		if err := sqlDB.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_down", "err": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	if d.Runtime != nil {
		api.Use(d.Runtime.AuthMiddleware())
	}
	{
		registerVersion(api, d)
		registerAuth(api, d)
		registerAccounts(api, d)
		registerSites(api, d)
		registerSiteAccountBundles(api, d)
		registerCaptchas(api, d)
		registerNotifications(api, d)
		registerAnnouncements(api, d)
		registerRates(api, d)
		registerMonitorLogs(api, d)
		registerDashboard(api, d)
		registerSettings(api, d)
		registerUpstreamSync(api, d)
	}

	if d.Frontend != nil {
		registerFrontend(r, d.Frontend)
	}
}

// registerFrontend 把嵌入的前端 dist 挂在根路径，并处理 SPA fallback：
//
//   - GET /assets/*  → 直接返回文件
//   - GET /          → 返回 index.html
//   - GET /accounts  → 返回 index.html（React Router 客户端路由）
//
// /api/*、/healthz 都已被前面的具体路由占了，不会走到这里。
// 安全起见仍然做一次前缀拦截，避免任何意外情况下"未鉴权读 index.html"压到 /api 上。
func registerFrontend(r *gin.Engine, dist fs.FS) {
	fileServer := http.FileServer(http.FS(dist))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// 永远不让 SPA fallback 覆盖 API / 健康检查路径。
		if strings.HasPrefix(path, "/api/") || path == "/healthz" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		// 文件存在就直接 serve，否则回落到 index.html。
		clean := strings.TrimPrefix(path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(dist, clean); err != nil {
			c.Request.URL.Path = "/"
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

// fail 统一错误响应。
func fail(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}
