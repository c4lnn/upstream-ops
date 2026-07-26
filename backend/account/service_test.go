package account

import (
	"testing"

	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newAccountServiceTest(t *testing.T) (*Service, *storage.UpstreamSites) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	sites := storage.NewUpstreamSites(db)
	service := NewService(storage.NewUpstreamAccounts(db), storage.NewAuthSessions(db), storage.NewCaptchas(db), storage.NewRates(db), storage.NewMonitorLogs(db), cipher)
	service.SetSites(sites)
	return service, sites
}

func TestCreateResolvesSiteOwnership(t *testing.T) {
	service, sites := newAccountServiceTest(t)
	site := &storage.UpstreamSite{Name: "demo", Type: storage.UpstreamTypeNewAPI, BaseURL: "https://example.com"}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	account, err := service.Create(CreateInput{SiteID: site.ID, Alias: "primary", Username: "user", Password: "secret", MonitorEnabled: true})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if account.SiteID != site.ID || account.Alias != "primary" {
		t.Fatalf("account = %#v", account)
	}
	target, err := service.Resolve(t.Context(), account)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if target.BaseURL != site.BaseURL || target.Type != "newapi" || target.Password != "secret" {
		t.Fatalf("target = %#v", target)
	}
}

func TestCreateRequiresSite(t *testing.T) {
	service, _ := newAccountServiceTest(t)
	if _, err := service.Create(CreateInput{Alias: "primary", Password: "secret"}); err == nil {
		t.Fatal("expected site ownership error")
	}
}
