package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

func TestRateChangesAggregateSameBatchAndExposeSiteAccountNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	rates := storage.NewRates(db)
	firstSite := createTestSite(t, sites, "first", storage.UpstreamTypeNewAPI, "https://first.example.com")
	secondSite := createTestSite(t, sites, "second", storage.UpstreamTypeSub2API, "https://second.example.com")
	firstPrimary := createTestAccount(t, sites, firstSite.ID, "primary")
	firstBackup := createTestAccount(t, sites, firstSite.ID, "backup")
	secondPrimary := createTestAccount(t, sites, secondSite.ID, "primary")
	now := time.Now()

	appendChange := func(row storage.RateChangeLog) {
		t.Helper()
		if err := rates.AppendChange(&row); err != nil {
			t.Fatalf("append change: %v", err)
		}
	}
	// run-a：first 站点两个账号命中同一变化 → 聚合为一条
	appendChange(storage.RateChangeLog{SiteID: firstSite.ID, AccountID: firstPrimary.ID, ScanRunID: "run-a", StableGroupKey: "name:gpt", ChangeType: "added", ModelName: "gpt", NewRatio: 1, ChangedAt: now})
	appendChange(storage.RateChangeLog{SiteID: firstSite.ID, AccountID: firstBackup.ID, ScanRunID: "run-a", StableGroupKey: "name:gpt", ChangeType: "added", ModelName: "gpt", NewRatio: 1, ChangedAt: now.Add(time.Second)})
	// run-b：second 站点同名账号 primary，数值与 run-a 相同 → 不得跨站点/跨批次聚合
	appendChange(storage.RateChangeLog{SiteID: secondSite.ID, AccountID: secondPrimary.ID, ScanRunID: "run-b", StableGroupKey: "name:gpt", ChangeType: "added", ModelName: "gpt", NewRatio: 1, ChangedAt: now.Add(2 * time.Second)})
	// run-b：已删除账号的孤儿行
	appendChange(storage.RateChangeLog{SiteID: secondSite.ID, AccountID: secondPrimary.ID + 100, ScanRunID: "run-b", StableGroupKey: "name:gpt", ChangeType: "added", ModelName: "gpt", NewRatio: 3, ChangedAt: now.Add(3 * time.Second)})

	router := gin.New()
	registerRates(router.Group("/api"), &Deps{Rates: rates, Sites: sites, Accounts: accounts})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rate-changes?page=1&page_size=20", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("rate-changes status = %d body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Data struct {
			Items []rateChangeGroupItem `json:"items"`
			Total int64                 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode rate-changes: %v", err)
	}
	if payload.Data.Total != 3 || len(payload.Data.Items) != 3 {
		t.Fatalf("total = %d items = %d, want 3 aggregated groups", payload.Data.Total, len(payload.Data.Items))
	}

	byKey := make(map[string]rateChangeGroupItem, len(payload.Data.Items))
	for _, item := range payload.Data.Items {
		if item.SiteID == 0 || item.ID == 0 {
			t.Fatalf("item lacks stable ids: %#v", item)
		}
		byKey[fmt.Sprintf("%s|%s|%g", item.SiteName, item.ScanRunID, item.NewRatio)] = item
	}

	merged, ok := byKey["first|run-a|1"]
	if !ok || len(merged.Accounts) != 2 {
		t.Fatalf("same-batch change should aggregate into one item: %#v", payload.Data.Items)
	}
	if merged.Accounts[0].AccountAlias != "primary" || merged.Accounts[1].AccountAlias != "backup" {
		t.Fatalf("merged accounts = %#v", merged.Accounts)
	}

	crossSite, ok := byKey["second|run-b|1"]
	if !ok || len(crossSite.Accounts) != 1 || crossSite.Accounts[0].AccountAlias != "primary" {
		t.Fatalf("same-alias account on another site must stay separate: %#v", crossSite)
	}

	orphan, ok := byKey["second|run-b|3"]
	if !ok || len(orphan.Accounts) != 1 {
		t.Fatalf("orphan change missing: %#v", payload.Data.Items)
	}
	if orphan.Accounts[0].AccountID != secondPrimary.ID+100 || orphan.Accounts[0].AccountAlias != "" {
		t.Fatalf("deleted account must keep stable id with empty alias: %#v", orphan.Accounts)
	}
}
