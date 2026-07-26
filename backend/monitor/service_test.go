package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/bejix/upstream-ops/backend/account"
	_ "github.com/bejix/upstream-ops/backend/connector/newapi"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

func TestSyncSiteAnnouncementsUsesOnlyDefaultAccount(t *testing.T) {
	db := openTestDB(t)
	accounts := storage.NewUpstreamAccounts(db)
	sites := storage.NewUpstreamSites(db)
	sessions := storage.NewAuthSessions(db)
	announcements := storage.NewUpstreamAnnouncements(db)
	rates := storage.NewRates(db)
	monitorLogs := storage.NewMonitorLogs(db)
	cipher, err := crypto.NewCipher("monitor-test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	var defaultAttempts atomic.Int32
	var backupAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Cookie") {
		case "session=default":
			defaultAttempts.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "session=backup":
			backupAttempts.Add(1)
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":2}}`))
		default:
			http.Error(w, "missing session", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)

	site := &storage.UpstreamSite{Name: "site", Type: storage.UpstreamTypeNewAPI, BaseURL: server.URL}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	defaultAccount := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: "default", Username: "default", CredentialMode: storage.CredentialModeToken,
		PasswordCipher: encrypt(t, cipher, `{"cookie":"session=default","user_id":"1"}`), MonitorEnabled: true,
	}
	backupAccount := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: "backup", Username: "backup", CredentialMode: storage.CredentialModeToken,
		PasswordCipher: encrypt(t, cipher, `{"cookie":"session=backup","user_id":"2"}`), MonitorEnabled: true,
	}
	if err := sites.AddAccount(defaultAccount); err != nil {
		t.Fatalf("add default account: %v", err)
	}
	if err := sites.AddAccount(backupAccount); err != nil {
		t.Fatalf("add backup account: %v", err)
	}
	site, err = sites.FindByID(site.ID)
	if err != nil {
		t.Fatalf("reload site: %v", err)
	}

	accountSvc := account.NewService(accounts, sessions, storage.NewCaptchas(db), rates, monitorLogs, cipher)
	accountSvc.SetSites(sites)
	service := NewService(accounts, announcements, rates, monitorLogs, accountSvc,
		notify.NewDispatcher(storage.NewNotifications(db), cipher, testLogger(), notify.Policy{SendMaxAttempts: 1}), testLogger())
	service.SetSites(sites)

	if err := service.syncSiteAnnouncements(context.Background(), site); err == nil {
		t.Fatal("expected default account authentication failure")
	}
	if defaultAttempts.Load() != 1 {
		t.Fatalf("default account attempts = %d, want 1", defaultAttempts.Load())
	}
	if backupAttempts.Load() != 0 {
		t.Fatalf("backup account attempts = %d, want 0; cross-account fallback is forbidden", backupAttempts.Load())
	}
}

func TestSiteRateChangeKeepsSiteAndAccountScope(t *testing.T) {
	account := &storage.UpstreamAccount{ID: 7, SiteID: 3, Alias: "primary"}
	change := siteRateChange(account, "run-1", "id:1", "ratio_changed", notify.RateChange{GroupName: "default"})
	if change.SiteID != 3 || change.AccountID != 7 || change.AccountName != "primary" {
		t.Fatalf("scope = %#v", change)
	}
}

func TestCheckSubscriptionUsageSkipsNonSub2APISite(t *testing.T) {
	db := openTestDB(t)
	accounts := storage.NewUpstreamAccounts(db)
	sites := storage.NewUpstreamSites(db)
	cipher, err := crypto.NewCipher("monitor-test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	site := &storage.UpstreamSite{Name: "newapi", Type: storage.UpstreamTypeNewAPI, BaseURL: "https://example.com"}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	upstreamAccount := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: "account", Username: "account", PasswordCipher: encrypt(t, cipher, "password"),
		CredentialMode: storage.CredentialModePassword, MonitorEnabled: true, SubscriptionEnabled: true,
	}
	if err := accounts.Create(upstreamAccount); err != nil {
		t.Fatalf("create account: %v", err)
	}
	accountSvc := account.NewService(accounts, storage.NewAuthSessions(db), storage.NewCaptchas(db), storage.NewRates(db), storage.NewMonitorLogs(db), cipher)
	accountSvc.SetSites(sites)
	service := NewService(accounts, storage.NewUpstreamAnnouncements(db), storage.NewRates(db), storage.NewMonitorLogs(db), accountSvc,
		notify.NewDispatcher(storage.NewNotifications(db), cipher, testLogger(), notify.Policy{SubscriptionDailyRemainingThresholdPct: 10}), testLogger())
	service.SetSites(sites)
	if err := service.CheckSubscriptionUsageAlerts(context.Background(), upstreamAccount); err != nil {
		t.Fatalf("non-Sub2API account should be skipped: %v", err)
	}
}

func TestAccountNotificationsDistinguishSameAliasAcrossSites(t *testing.T) {
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	notifications := storage.NewNotifications(db)
	cipher, err := crypto.NewCipher("monitor-test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	firstSite := &storage.UpstreamSite{Name: "first", Type: storage.UpstreamTypeNewAPI, BaseURL: "https://first.example.com"}
	if err := sites.Create(firstSite); err != nil {
		t.Fatalf("create first site: %v", err)
	}
	secondSite := &storage.UpstreamSite{Name: "second", Type: storage.UpstreamTypeSub2API, BaseURL: "https://second.example.com"}
	if err := sites.Create(secondSite); err != nil {
		t.Fatalf("create second site: %v", err)
	}
	firstAccount := &storage.UpstreamAccount{
		SiteID: firstSite.ID, Alias: "primary", Username: "first-user",
		PasswordCipher: encrypt(t, cipher, "password"),
	}
	if err := sites.AddAccount(firstAccount); err != nil {
		t.Fatalf("add first account: %v", err)
	}
	secondAccount := &storage.UpstreamAccount{
		SiteID: secondSite.ID, Alias: "primary", Username: "second-user",
		PasswordCipher: encrypt(t, cipher, "password"),
	}
	if err := sites.AddAccount(secondAccount); err != nil {
		t.Fatalf("add second account: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	channel := &storage.NotificationChannel{
		Name: "hook", Type: storage.NotifyWebhook,
		ConfigCipher: encrypt(t, cipher, `{"url":"`+server.URL+`"}`), Enabled: true,
	}
	if err := notifications.CreateChannel(channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := &Service{
		sites:      sites,
		dispatcher: notify.NewDispatcher(notifications, cipher, testLogger(), notify.Policy{SendMaxAttempts: 1}),
		log:        testLogger(),
	}
	service.notifyError(context.Background(), firstAccount, storage.EventLoginFailed, "登录失败", errors.New("boom"))
	service.notifyError(context.Background(), secondAccount, storage.EventLoginFailed, "登录失败", errors.New("boom"))

	logs, err := notifications.ListLogs(10)
	if err != nil {
		t.Fatalf("list notification logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %#v", logs)
	}
	subjects := make(map[string]bool, len(logs))
	for _, item := range logs {
		if !item.Success {
			t.Fatalf("notification send failed: %#v", item)
		}
		subjects[item.Subject] = true
	}
	if !subjects["first / primary 登录失败"] || !subjects["second / primary 登录失败"] {
		t.Fatalf("subjects lack site identity: %#v", subjects)
	}

	orphan := &storage.UpstreamAccount{ID: 999, SiteID: 9999, Alias: "primary"}
	if got := service.accountLabel(orphan); got != "primary" {
		t.Fatalf("orphan label = %q, want bare alias fallback", got)
	}
}

func TestScanAllBalancesStopsBeforeListingWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := &Service{log: testLogger()}
	service.ScanAllBalances(ctx)
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{Driver: storage.DBDriverSQLite, Path: filepath.Join(t.TempDir(), "monitor-test.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func encrypt(t *testing.T, cipher *crypto.Cipher, value string) string {
	t.Helper()
	result, err := cipher.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return result
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
