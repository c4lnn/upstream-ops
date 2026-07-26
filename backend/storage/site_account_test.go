package storage

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSiteAccountTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := NormalizeBaseURL(" HTTPS://EXAMPLE.com/api/ ")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got != "https://example.com/api" {
		t.Fatalf("base url = %q", got)
	}
	got, err = NormalizeBaseURL("https://example.com/api%20v1/")
	if err != nil || got != "https://example.com/api%20v1" {
		t.Fatalf("encoded path normalization = %q, err=%v", got, err)
	}
	for _, raw := range []string{"example.com", "ftp://example.com", "https://u@example.com", "https://example.com/?q=1"} {
		if _, err := NormalizeBaseURL(raw); err == nil {
			t.Fatalf("NormalizeBaseURL(%q) succeeded", raw)
		}
	}
}

func TestSiteAccountLifecycle(t *testing.T) {
	db := newSiteAccountTestDB(t)
	sites := NewUpstreamSites(db)
	site := &UpstreamSite{Name: "Demo", Type: UpstreamTypeNewAPI, BaseURL: "https://example.com/"}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	first := &UpstreamAccount{SiteID: site.ID, Alias: "Primary", Username: "u", PasswordCipher: "cipher"}
	if err := sites.AddAccount(first); err != nil {
		t.Fatalf("add first account: %v", err)
	}
	second := &UpstreamAccount{SiteID: site.ID, Alias: "Backup", Username: "v", PasswordCipher: "cipher"}
	if err := sites.AddAccount(second); err != nil {
		t.Fatalf("add second account: %v", err)
	}
	stored, err := sites.FindByID(site.ID)
	if err != nil {
		t.Fatalf("find site: %v", err)
	}
	if stored.BaseURL != "https://example.com" || stored.DefaultAccountID != first.ID {
		t.Fatalf("stored site = %#v", stored)
	}
	if err := sites.SetDefaultAccount(site.ID, second.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if err := sites.DeleteAccount(second.ID, first.ID); err != nil {
		t.Fatalf("delete default account: %v", err)
	}
}

func TestUpdateBaseURLClearsSessionsAndPausesMonitoring(t *testing.T) {
	db := newSiteAccountTestDB(t)
	sites := NewUpstreamSites(db)
	site := &UpstreamSite{Name: "Demo", Type: UpstreamTypeNewAPI, BaseURL: "https://one.example.com"}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	account := &UpstreamAccount{SiteID: site.ID, Alias: "Primary", Username: "u", PasswordCipher: "cipher", MonitorEnabled: true}
	if err := sites.AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := NewAuthSessions(db).Upsert(&AuthSession{AccountID: account.ID}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if _, err := sites.UpdateBaseURL(site.ID, "https://two.example.com", false); err == nil {
		t.Fatal("expected confirmation error")
	}
	affected, err := sites.UpdateBaseURL(site.ID, "https://two.example.com", true)
	if err != nil || affected != 1 {
		t.Fatalf("update base url: affected=%d err=%v", affected, err)
	}
	updated, err := NewUpstreamAccounts(db).FindByID(account.ID)
	if err != nil || updated.MonitorEnabled {
		t.Fatalf("account after endpoint update: %#v err=%v", updated, err)
	}
	session, err := NewAuthSessions(db).FindByAccount(account.ID)
	if err != nil || session != nil {
		t.Fatalf("session after endpoint update: %#v err=%v", session, err)
	}
}

func TestAutoMigrateRejectsLegacyChannels(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:legacy-schema?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec("CREATE TABLE channels (id integer primary key)").Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := AutoMigrate(db); err == nil {
		t.Fatal("expected legacy schema error")
	}
}
