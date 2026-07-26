package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/runtimeconfig"
	"github.com/gin-gonic/gin"
)

func TestSaveSettingsKeepsAppVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		App: config.AppConfig{
			Title:              "Old",
			NotificationPrefix: "[Old] ",
		},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	r := gin.New()
	api := r.Group("/api")
	registerSettings(api, &Deps{
		Runtime: runtimeconfig.New(path, "", nil, nil, nil, nil, nil, config.ProxyConfig{}, config.UpstreamConfig{}, nil),
	})

	body := `{
		"app":{"title":"New","notificationPrefix":"[New] "},
		"auth":{"enabled":false,"username":"admin","password":"","tokenSecret":"","sessionTTLHours":168},
		"scheduler":{"balanceCron":"37 */15 * * * *","rateCron":"13 */30 * * * *","balanceTimeoutSeconds":120,"rateTimeoutSeconds":240,"concurrency":4,"retention":{"cron":"0 17 3 * * *","monitorLogsDays":30,"balanceSnapshotsDays":90,"notificationLogsDays":90,"announcementsDays":90}},
		"notifications":{"batchRateChanges":true,"minChangePct":0,"balanceLowCooldownMinutes":60,"subscriptionDailyRemainingThresholdPct":0,"subscriptionWeeklyRemainingThresholdPct":0,"subscriptionMonthlyRemainingThresholdPct":0,"subscriptionExpiryThresholdHours":0,"subscriptionAlertCooldownMinutes":1440,"sendMaxAttempts":3},
		"proxy":{"enabled":true,"versionCheckEnabled":true,"protocol":"socks5","host":"127.0.0.1","port":1080,"username":"u","password":"p"},
		"upstream":{"timeoutSeconds":45,"userAgent":"custom-agent"}
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.App.Title != "New" {
		t.Fatalf("title = %q", got.App.Title)
	}
	if got.App.NotificationPrefix != "[New] " {
		t.Fatalf("notification prefix = %q", got.App.NotificationPrefix)
	}
	if !got.Proxy.Enabled || !got.Proxy.VersionCheckEnabled || got.Proxy.Protocol != "socks5" || got.Proxy.Host != "127.0.0.1" || got.Proxy.Port != 1080 || got.Proxy.Username != "u" || got.Proxy.Password != "p" {
		t.Fatalf("proxy = %#v", got.Proxy)
	}
	if got.Upstream.TimeoutSeconds != 45 || got.Upstream.UserAgent != "custom-agent" {
		t.Fatalf("upstream = %#v", got.Upstream)
	}
	if got.Scheduler.BalanceTimeoutSeconds != 120 || got.Scheduler.RateTimeoutSeconds != 240 {
		t.Fatalf("scheduler = %#v", got.Scheduler)
	}
}

func TestGetSettingsIncludesSchedulerTaskTimeoutDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, &config.Config{}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	r := gin.New()
	api := r.Group("/api")
	registerSettings(api, &Deps{
		Runtime: runtimeconfig.New(path, "", nil, nil, nil, nil, nil, config.ProxyConfig{}, config.UpstreamConfig{}, nil),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/settings/config", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var response struct {
		Data struct {
			Config struct {
				Scheduler config.SchedulerConfig `json:"scheduler"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Config.Scheduler.BalanceTimeoutSeconds != config.DefaultSchedulerTaskTimeoutSeconds || response.Data.Config.Scheduler.RateTimeoutSeconds != config.DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("scheduler = %#v", response.Data.Config.Scheduler)
	}
}

func TestSaveSettingsNormalizesNonPositiveSchedulerTaskTimeouts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, &config.Config{}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	r := gin.New()
	api := r.Group("/api")
	registerSettings(api, &Deps{
		Runtime: runtimeconfig.New(path, "", nil, nil, nil, nil, nil, config.ProxyConfig{}, config.UpstreamConfig{}, nil),
	})

	body := `{
		"app":{"title":"UpstreamOps","notificationPrefix":"[AI] "},
		"auth":{"enabled":false,"username":"admin","password":"","tokenSecret":"","sessionTTLHours":168},
		"scheduler":{"balanceCron":"","rateCron":"","balanceTimeoutSeconds":0,"rateTimeoutSeconds":-1,"concurrency":4,"retention":{"cron":"","monitorLogsDays":30,"balanceSnapshotsDays":90,"notificationLogsDays":90,"announcementsDays":90}},
		"notifications":{"batchRateChanges":true,"minChangePct":0,"balanceLowCooldownMinutes":60,"subscriptionDailyRemainingThresholdPct":0,"subscriptionWeeklyRemainingThresholdPct":0,"subscriptionMonthlyRemainingThresholdPct":0,"subscriptionExpiryThresholdHours":0,"subscriptionAlertCooldownMinutes":1440,"sendMaxAttempts":3},
		"proxy":{"enabled":false,"versionCheckEnabled":false,"protocol":"http","host":"","port":0,"username":"","password":""},
		"upstream":{"timeoutSeconds":30,"userAgent":"upstream-ops/0.1"}
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.Scheduler.BalanceTimeoutSeconds != config.DefaultSchedulerTaskTimeoutSeconds || got.Scheduler.RateTimeoutSeconds != config.DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("scheduler = %#v", got.Scheduler)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(saved), "balanceTimeoutSeconds: 300") || !strings.Contains(string(saved), "rateTimeoutSeconds: 300") {
		t.Fatalf("scheduler timeouts were not normalized before save: %s", saved)
	}
}
