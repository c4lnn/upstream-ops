package api

import (
	"path/filepath"
	"testing"

	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "api-test.db"),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func createTestSite(t *testing.T, sites *storage.UpstreamSites, name string, kind storage.UpstreamType, baseURL string) *storage.UpstreamSite {
	t.Helper()
	site := &storage.UpstreamSite{Name: name, Type: kind, BaseURL: baseURL}
	if err := sites.Create(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	return site
}

func createTestAccount(t *testing.T, sites *storage.UpstreamSites, siteID uint, alias string) *storage.UpstreamAccount {
	t.Helper()
	account := &storage.UpstreamAccount{
		SiteID:         siteID,
		Alias:          alias,
		Username:       alias + "-user",
		PasswordCipher: "ciphertext",
		MonitorEnabled: true,
	}
	if err := sites.AddAccount(account); err != nil {
		t.Fatalf("create account: %v", err)
	}
	return account
}
