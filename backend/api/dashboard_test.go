package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/progress"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

type accountMonitorStub struct{}

func (accountMonitorStub) RefreshBalance(ctx context.Context, account *storage.UpstreamAccount) error {
	progress.Start(ctx, progress.StageBalance, "balance")
	return nil
}
func (accountMonitorStub) RefreshRates(ctx context.Context, account *storage.UpstreamAccount) error {
	progress.Start(ctx, progress.StageRates, "rates")
	return nil
}
func (accountMonitorStub) CheckSubscriptionUsageAlerts(context.Context, *storage.UpstreamAccount) error {
	return nil
}

func TestDashboardSummaryUsesAccountFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	rates := storage.NewRates(db)
	firstSite := createTestSite(t, sites, "first", storage.UpstreamTypeNewAPI, "https://first.example.com")
	secondSite := createTestSite(t, sites, "second", storage.UpstreamTypeSub2API, "https://second.example.com")
	first := createTestAccount(t, sites, firstSite.ID, "primary")
	second := createTestAccount(t, sites, secondSite.ID, "backup")
	if err := accounts.UpdateBalance(first.ID, 12.5, time.Now(), ""); err != nil {
		t.Fatalf("set first balance: %v", err)
	}
	if err := accounts.UpdateBalance(second.ID, 3.5, time.Now(), "failed"); err != nil {
		t.Fatalf("set second balance: %v", err)
	}
	if err := rates.AppendChange(&storage.RateChangeLog{SiteID: firstSite.ID, AccountID: first.ID, ScanRunID: "run-1", StableGroupKey: "name:gpt", ModelName: "gpt", NewRatio: 1}); err != nil {
		t.Fatalf("append change: %v", err)
	}

	router := gin.New()
	registerDashboard(router.Group("/api"), &Deps{Accounts: accounts, Sites: sites, Rates: rates})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/dashboard/summary", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("summary status = %d body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			TotalSites     int              `json:"total_sites"`
			TotalAccounts  int              `json:"total_accounts"`
			FailedAccounts int              `json:"failed_accounts"`
			Lowest         *dashboardLowest `json:"lowest_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if payload.Data.TotalSites != 2 || payload.Data.TotalAccounts != 2 || payload.Data.FailedAccounts != 1 {
		t.Fatalf("summary = %#v", payload.Data)
	}
	var summaryShape struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &summaryShape); err != nil {
		t.Fatalf("decode summary shape: %v", err)
	}
	if _, exists := summaryShape.Data["accounts"]; exists {
		t.Fatalf("summary should not expose top-level account stats: %s", response.Body.String())
	}
	if payload.Data.Lowest == nil || payload.Data.Lowest.AccountID != second.ID || payload.Data.Lowest.AccountAlias != "backup" {
		t.Fatalf("lowest = %#v", payload.Data.Lowest)
	}
	if !strings.Contains(response.Body.String(), `"site_name":"first"`) || !strings.Contains(response.Body.String(), `"account_alias":"primary"`) {
		t.Fatalf("recent rate changes lack site/account names: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"accounts":[{"account_id":`) {
		t.Fatalf("recent rate changes are not aggregated groups: %s", response.Body.String())
	}
}

func TestAccountListAndSyncSSEUseAccountMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	site := createTestSite(t, sites, "source", storage.UpstreamTypeNewAPI, "https://source.example.com")
	low := createTestAccount(t, sites, site.ID, "low")
	low.AccountSortOrder = 1
	if err := accounts.Update(low); err != nil {
		t.Fatalf("update low: %v", err)
	}
	high := createTestAccount(t, sites, site.ID, "high")
	high.AccountSortOrder = 9
	if err := accounts.Update(high); err != nil {
		t.Fatalf("update high: %v", err)
	}

	router := gin.New()
	registerAccounts(router.Group("/api"), &Deps{Accounts: accounts, Sites: sites, Monitor: accountMonitorStub{}})
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/accounts?page=1&page_size=-1", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listResponse.Code, listResponse.Body.String())
	}
	var listPayload struct {
		Data struct {
			Items []storage.UpstreamAccount `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.Data.Items) != 2 || listPayload.Data.Items[0].Alias != "high" {
		t.Fatalf("accounts = %#v", listPayload.Data.Items)
	}

	syncResponse := httptest.NewRecorder()
	router.ServeHTTP(syncResponse, httptest.NewRequest(http.MethodPost, "/api/accounts/"+strconv.FormatUint(uint64(high.ID), 10)+"/sync", nil))
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("sync status = %d body = %s", syncResponse.Code, syncResponse.Body.String())
	}
	if !strings.Contains(syncResponse.Body.String(), `"account_id":`+strconv.FormatUint(uint64(high.ID), 10)) || !strings.Contains(syncResponse.Body.String(), `"account_alias":"high"`) {
		t.Fatalf("SSE lacks account metadata: %s", syncResponse.Body.String())
	}
}

func TestAccountScopedAuxiliaryEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	rates := storage.NewRates(db)
	logs := storage.NewMonitorLogs(db)
	notifies := storage.NewNotifications(db)
	now := time.Now()
	if err := rates.AppendChange(&storage.RateChangeLog{SiteID: 1, AccountID: 2, ModelName: "model-a", NewRatio: 1, ChangedAt: now}); err != nil {
		t.Fatalf("append rate change: %v", err)
	}
	if err := rates.AppendChange(&storage.RateChangeLog{SiteID: 1, AccountID: 3, ModelName: "model-b", NewRatio: 1, ChangedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("append second rate change: %v", err)
	}
	if err := logs.Append(&storage.MonitorLog{AccountID: 2, Job: storage.MonitorJobBalance, Success: true}); err != nil {
		t.Fatalf("append monitor log: %v", err)
	}
	channel := &storage.NotificationChannel{Name: "mail", Type: storage.NotifyEmail, ConfigCipher: "cipher", Enabled: true}
	if err := notifies.CreateChannel(channel); err != nil {
		t.Fatalf("create notification channel: %v", err)
	}
	if err := notifies.AppendLog(&storage.NotificationLog{NotificationChannelID: channel.ID, AccountID: 2, SiteID: 1, Event: storage.EventBalanceLow, Subject: "low", Success: true}); err != nil {
		t.Fatalf("append notification log: %v", err)
	}

	router := gin.New()
	deps := &Deps{Rates: rates, MonLogs: logs, Notifies: notifies}
	api := router.Group("/api")
	registerRates(api, deps)
	registerMonitorLogs(api, deps)
	registerNotifications(api, deps)

	for _, path := range []string{"/api/rate-changes?account_id=2", "/api/monitor-logs?account_id=2", "/api/notifications/logs"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body = %s", path, response.Code, response.Body.String())
		}
	}
}
