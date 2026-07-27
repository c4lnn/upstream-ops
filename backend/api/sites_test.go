package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/monitor"
	"github.com/bejix/upstream-ops/backend/progress"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

type siteSyncStub struct{ calledSiteID uint }

type blockingSiteSyncStub struct {
	siteSyncStub
	release chan struct{}
}

func (s *blockingSiteSyncStub) SyncSite(ctx context.Context, siteID uint) ([]monitor.SiteAccountSyncResult, error) {
	accountCtx := progress.WithScope(ctx, progress.Scope{
		Level: "account", SiteID: siteID, SiteName: "example", AccountID: 1, AccountAlias: "primary", Index: 1, Total: 1,
	})
	progress.Start(accountCtx, progress.StageBalance, "拉取余额…")
	<-s.release
	return []monitor.SiteAccountSyncResult{{AccountID: 1, AccountName: "primary", Success: true}}, nil
}

func (s *siteSyncStub) RefreshBalance(context.Context, *storage.UpstreamAccount) error { return nil }
func (s *siteSyncStub) RefreshRates(context.Context, *storage.UpstreamAccount) error   { return nil }
func (s *siteSyncStub) CheckSubscriptionUsageAlerts(context.Context, *storage.UpstreamAccount) error {
	return nil
}
func (s *siteSyncStub) SyncSite(ctx context.Context, siteID uint) ([]monitor.SiteAccountSyncResult, error) {
	s.calledSiteID = siteID
	accountCtx := progress.WithScope(ctx, progress.Scope{
		Level: "account", SiteID: siteID, SiteName: "example", AccountID: 1, AccountAlias: "primary", Index: 1, Total: 1,
	})
	progress.Start(accountCtx, progress.StageBalance, "拉取余额…")
	return []monitor.SiteAccountSyncResult{{AccountID: 1, AccountName: "primary", Success: true}}, errors.New("partial failure")
}

func newSiteAccountRouter(t *testing.T) (*gin.Engine, *storage.UpstreamSites, *storage.UpstreamAccounts, *storage.AuthSessions) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	sessions := storage.NewAuthSessions(db)
	cipher, err := crypto.NewCipher("sites-api-secret")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	service := account.NewService(accounts, sessions, storage.NewCaptchas(db), storage.NewRates(db), storage.NewMonitorLogs(db), cipher)
	service.SetSites(sites)
	router := gin.New()
	deps := &Deps{Accounts: accounts, Sites: sites, Sessions: sessions, Cipher: cipher, AccountSvc: service}
	api := router.Group("/api")
	registerSites(api, deps)
	registerAccounts(api, deps)
	return router, sites, accounts, sessions
}

func TestSiteAccountAPIUsesSiteOwnedConnection(t *testing.T) {
	router, sites, accounts, _ := newSiteAccountRouter(t)

	createSite := requestJSON(t, router, http.MethodPost, "/api/sites", map[string]any{
		"name": "source", "type": "newapi", "base_url": "https://SOURCE.example.com/", "sort_order": 3,
	})
	if createSite.Code != http.StatusOK {
		t.Fatalf("create site status = %d body = %s", createSite.Code, createSite.Body.String())
	}
	var siteResponse struct {
		Data storage.UpstreamSite `json:"data"`
	}
	if err := json.Unmarshal(createSite.Body.Bytes(), &siteResponse); err != nil {
		t.Fatalf("decode site: %v", err)
	}
	if siteResponse.Data.BaseURL != "https://source.example.com" {
		t.Fatalf("normalized base url = %q", siteResponse.Data.BaseURL)
	}

	createAccount := requestJSON(t, router, http.MethodPost, "/api/sites/"+strconv.Itoa(int(siteResponse.Data.ID))+"/accounts", map[string]any{
		"alias": "primary", "username": "admin", "password": "secret", "credential_mode": "password", "sort_order": 7,
	})
	if createAccount.Code != http.StatusOK {
		t.Fatalf("create account status = %d body = %s", createAccount.Code, createAccount.Body.String())
	}
	if strings.Contains(createAccount.Body.String(), "secret") || strings.Contains(createAccount.Body.String(), "password_cipher") {
		t.Fatalf("sensitive credential leaked: %s", createAccount.Body.String())
	}
	var accountResponse struct {
		Data storage.UpstreamAccount `json:"data"`
	}
	if err := json.Unmarshal(createAccount.Body.Bytes(), &accountResponse); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if accountResponse.Data.SiteID != siteResponse.Data.ID || accountResponse.Data.Alias != "primary" || accountResponse.Data.AccountSortOrder != 7 {
		t.Fatalf("account = %#v", accountResponse.Data)
	}

	for _, forbidden := range []string{"type", "base_url", "site_url", "site_id"} {
		body := map[string]any{"alias": "blocked", "username": "admin", "password": "secret", forbidden: "blocked"}
		response := requestJSON(t, router, http.MethodPost, "/api/sites/"+strconv.Itoa(int(siteResponse.Data.ID))+"/accounts", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("forbidden create field %s status = %d body = %s", forbidden, response.Code, response.Body.String())
		}
	}
	updateForbidden := requestJSON(t, router, http.MethodPut, "/api/accounts/"+strconv.Itoa(int(accountResponse.Data.ID)), map[string]any{"site_id": 999})
	if updateForbidden.Code != http.StatusBadRequest {
		t.Fatalf("forbidden update status = %d body = %s", updateForbidden.Code, updateForbidden.Body.String())
	}

	rootCreate := requestJSON(t, router, http.MethodPost, "/api/accounts", map[string]any{"alias": "nope"})
	if rootCreate.Code != http.StatusNotFound {
		t.Fatalf("root account creation status = %d", rootCreate.Code)
	}
	oldRoute := requestJSON(t, router, http.MethodGet, "/api/channels", nil)
	if oldRoute.Code != http.StatusNotFound {
		t.Fatalf("old channels route status = %d", oldRoute.Code)
	}
	moveRoute := requestJSON(t, router, http.MethodPost, "/api/sites/1/accounts/1/move", map[string]any{})
	if moveRoute.Code != http.StatusNotFound {
		t.Fatalf("account move route status = %d", moveRoute.Code)
	}

	stored, err := accounts.FindByID(accountResponse.Data.ID)
	if err != nil || stored.SiteID != siteResponse.Data.ID {
		t.Fatalf("stored account = %#v, err = %v", stored, err)
	}
	storedSite, err := sites.FindByID(siteResponse.Data.ID)
	if err != nil || storedSite.DefaultAccountID != accountResponse.Data.ID {
		t.Fatalf("default account = %#v, err = %v", storedSite, err)
	}
}

func TestSiteBaseURLChangeRequiresConfirmationAndInvalidatesAccounts(t *testing.T) {
	router, sites, accounts, sessions := newSiteAccountRouter(t)
	site := createTestSite(t, sites, "source", storage.UpstreamTypeSub2API, "https://old.example.com")
	first := createTestAccount(t, sites, site.ID, "primary")
	_ = createTestAccount(t, sites, site.ID, "backup")
	if err := sessions.Upsert(&storage.AuthSession{AccountID: first.ID, UserID: "remote-user", LastLoginAt: ptrTime(time.Now())}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	withoutConfirmation := requestJSON(t, router, http.MethodPut, "/api/sites/"+strconv.Itoa(int(site.ID)), map[string]any{"base_url": "https://new.example.com"})
	if withoutConfirmation.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed update status = %d body = %s", withoutConfirmation.Code, withoutConfirmation.Body.String())
	}
	unchanged, _ := sites.FindByID(site.ID)
	if unchanged.BaseURL != "https://old.example.com" {
		t.Fatalf("base url changed without confirmation: %q", unchanged.BaseURL)
	}
	if session, _ := sessions.FindByAccount(first.ID); session == nil {
		t.Fatal("session was cleared without confirmation")
	}

	confirmed := requestJSON(t, router, http.MethodPut, "/api/sites/"+strconv.Itoa(int(site.ID)), map[string]any{
		"base_url": "https://new.example.com/", "confirm_base_url_change": true,
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmed update status = %d body = %s", confirmed.Code, confirmed.Body.String())
	}
	var payload struct {
		Data struct {
			BaseURL              string `json:"base_url"`
			AffectedAccountCount int    `json:"affected_account_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode confirmed response: %v", err)
	}
	if payload.Data.BaseURL != "https://new.example.com" || payload.Data.AffectedAccountCount != 2 {
		t.Fatalf("confirmed response = %#v", payload.Data)
	}
	if session, _ := sessions.FindByAccount(first.ID); session != nil {
		t.Fatal("session was not cleared")
	}
	for _, id := range []uint{first.ID, first.ID + 1} {
		item, err := accounts.FindByID(id)
		if err != nil || item.MonitorEnabled {
			t.Fatalf("account %d monitor state = %#v, err = %v", id, item, err)
		}
	}
}

func TestSiteSyncReturnsPartialAccountResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &siteSyncStub{}
	router := gin.New()
	registerSites(router.Group("/api"), &Deps{Sites: storage.NewUpstreamSites(openTestDB(t)), Monitor: stub})
	response := requestJSON(t, router, http.MethodPost, "/api/sites/9/sync", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("sync status = %d body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Items        []monitor.SiteAccountSyncResult `json:"items"`
			Partial      bool                            `json:"partial"`
			Status       string                          `json:"status"`
			SuccessCount int                             `json:"success_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if stub.calledSiteID != 9 || !payload.Data.Partial || payload.Data.Status != "partial" || payload.Data.SuccessCount != 1 || len(payload.Data.Items) != 1 || payload.Data.Items[0].AccountID != 1 {
		t.Fatalf("sync payload = %#v, site = %d", payload.Data, stub.calledSiteID)
	}
}

func TestSiteSyncStreamEmitsAccountProgressAndOperationSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &siteSyncStub{}
	router := gin.New()
	registerSites(router.Group("/api"), &Deps{Sites: storage.NewUpstreamSites(openTestDB(t)), Monitor: stub})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/sites/9/sync-stream", nil)
	request.Header.Set("Accept", "text/event-stream")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status = %d content-type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	for _, want := range []string{`"account_id":1`, `"scope":"account"`, `"scope":"operation"`, `"status":"partial"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %s: %s", want, body)
		}
	}
}

func TestSiteSyncStreamFlushesProgressBeforeBatchCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &blockingSiteSyncStub{release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(stub.release)
		}
	}()
	router := gin.New()
	registerSites(router.Group("/api"), &Deps{Sites: storage.NewUpstreamSites(openTestDB(t)), Monitor: stub})
	server := httptest.NewServer(router)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/sites/9/sync-stream", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read first progress frame: %v", err)
	}
	if !strings.Contains(line, `"stage":"balance"`) || !strings.Contains(line, `"account_id":1`) {
		t.Fatalf("first progress line = %s", line)
	}
	close(stub.release)
	released = true
}

func TestSiteListAggregatesPerSiteBalancesForSameAliasAccounts(t *testing.T) {
	router, sites, accounts, _ := newSiteAccountRouter(t)

	firstSite := createTestSite(t, sites, "first", storage.UpstreamTypeNewAPI, "https://first.example.com")
	secondSite := createTestSite(t, sites, "second", storage.UpstreamTypeSub2API, "https://second.example.com")
	firstPrimary := createTestAccount(t, sites, firstSite.ID, "primary")
	firstBackup := createTestAccount(t, sites, firstSite.ID, "backup")
	secondPrimary := createTestAccount(t, sites, secondSite.ID, "primary")

	if err := accounts.UpdateBalance(firstPrimary.ID, 12.5, time.Now(), ""); err != nil {
		t.Fatalf("set first primary balance: %v", err)
	}
	if err := accounts.UpdateCosts(firstPrimary.ID, 3, 30); err != nil {
		t.Fatalf("set first primary costs: %v", err)
	}
	if err := accounts.UpdateBalance(firstBackup.ID, 7.5, time.Now(), ""); err != nil {
		t.Fatalf("set first backup balance: %v", err)
	}
	if err := accounts.UpdateCosts(firstBackup.ID, 1.5, 15); err != nil {
		t.Fatalf("set first backup costs: %v", err)
	}
	if err := accounts.UpdateBalance(secondPrimary.ID, 2, time.Now(), "login failed"); err != nil {
		t.Fatalf("set second primary balance: %v", err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sites", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list sites status = %d body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []storage.UpstreamSiteSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode sites: %v", err)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("sites = %#v", payload.Data)
	}
	byName := make(map[string]storage.UpstreamSiteSummary, len(payload.Data))
	for _, summary := range payload.Data {
		byName[summary.Name] = summary
	}

	first, ok := byName["first"]
	if !ok || first.AccountCount != 2 || first.ErrorAccountCount != 0 {
		t.Fatalf("first summary = %#v", first)
	}
	if first.TotalBalance == nil || *first.TotalBalance != 20 {
		t.Fatalf("first total balance = %#v, want own accounts only", first.TotalBalance)
	}
	if first.TodayCost == nil || *first.TodayCost != 4.5 {
		t.Fatalf("first today cost = %#v", first.TodayCost)
	}

	second, ok := byName["second"]
	if !ok || second.AccountCount != 1 || second.ErrorAccountCount != 1 {
		t.Fatalf("second summary = %#v", second)
	}
	if second.TotalBalance == nil || *second.TotalBalance != 2 {
		t.Fatalf("second total balance = %#v, must not absorb same-alias account from first", second.TotalBalance)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func requestJSON(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
