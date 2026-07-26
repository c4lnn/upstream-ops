package accountbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	appcrypto "github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newBundleTestService(t *testing.T, secret string) (*Service, *gorm.DB, *appcrypto.Cipher) {
	t.Helper()
	dsn := fmt.Sprintf("file:site-account-bundle-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	cipher, err := appcrypto.NewCipher(secret)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(db, cipher), db, cipher
}

func createBundleTestSite(t *testing.T, db *gorm.DB, cipher *appcrypto.Cipher, name string, siteType storage.UpstreamType, baseURL, alias, credential string) (*storage.UpstreamSite, *storage.UpstreamAccount) {
	t.Helper()
	site := &storage.UpstreamSite{Name: name, Type: siteType, BaseURL: baseURL, SortOrder: 2}
	if err := storage.NewUpstreamSites(db).Create(site); err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt(credential)
	if err != nil {
		t.Fatal(err)
	}
	account := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: alias, Username: "user", AccountSortOrder: 3,
		PasswordCipher: encrypted, CredentialMode: storage.CredentialModePassword,
		RechargeMultiplierMode: "divide", MonitorEnabled: true,
	}
	if err := storage.NewUpstreamAccounts(db).Create(account); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewUpstreamSites(db).SetDefaultAccount(site.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	site.DefaultAccountID = account.ID
	return site, account
}

func encodeBundle(t *testing.T, bundle Bundle) []byte {
	t.Helper()
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testImportBundle(baseURL string) Bundle {
	return Bundle{
		Schema: BundleSchema, Version: BundleVersion, ExportedAt: time.Now().UTC(),
		Sites: []SiteConfig{{
			Key: "site-1", Name: "Demo", Type: storage.UpstreamTypeNewAPI, BaseURL: baseURL, SortOrder: 5,
			DefaultAccount: "account-2",
			Accounts: []AccountConfig{
				{
					Key: "account-1", Alias: "Primary", AccountSortOrder: 2,
					CredentialMode: storage.CredentialModePassword, CaptchaConfigName: "missing-captcha",
					RechargeMultiplierMode: "divide", MonitorEnabled: true,
				},
				{
					Key: "account-2", Alias: "Backup", AccountSortOrder: 1,
					CredentialMode:         storage.CredentialModePassword,
					RechargeMultiplierMode: "multiply", MonitorEnabled: true,
				},
			},
		}},
	}
}

func findBundleAccount(t *testing.T, db *gorm.DB, siteID uint, alias string) *storage.UpstreamAccount {
	t.Helper()
	var account storage.UpstreamAccount
	if err := db.Where("site_id = ? AND alias = ?", siteID, alias).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	return &account
}

func TestExportRedactedProtectedAndPartial(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "source-secret")
	firstSite, firstAccount := createBundleTestSite(t, db, cipher, "First", storage.UpstreamTypeNewAPI, "https://first.example.com/", "Primary", "plain-password")
	secondSite, _ := createBundleTestSite(t, db, cipher, "Second", storage.UpstreamTypeSub2API, "https://second.example.com", "Other", "second-password")
	balance := 88.5
	if err := db.Model(firstAccount).Updates(map[string]any{"last_balance": balance, "last_error": "runtime-error"}).Error; err != nil {
		t.Fatal(err)
	}

	redacted, err := service.Export(context.Background(), ExportOptions{SiteIDs: []uint{firstSite.ID}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(redacted)
	for _, forbidden := range []string{"plain-password", firstAccount.PasswordCipher, "last_balance", "last_error", "auth_sessions"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("redacted export leaked %q: %s", forbidden, text)
		}
	}
	bundle, err := Decode(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sites) != 1 || bundle.Sites[0].Name != "First" || bundle.Sites[0].BaseURL != "https://first.example.com" || bundle.Credentials != nil {
		t.Fatalf("unexpected partial export: %#v", bundle)
	}
	if account := bundle.Sites[0].Accounts[0]; account.Alias != "Primary" || account.CredentialIncluded {
		t.Fatalf("unexpected redacted account: %#v", account)
	}
	var raw map[string]any
	if err := json.Unmarshal(redacted, &raw); err != nil {
		t.Fatal(err)
	}
	accounts := raw["sites"].([]any)[0].(map[string]any)["accounts"].([]any)
	for _, field := range []string{"type", "site_url", "base_url"} {
		if _, found := accounts[0].(map[string]any)[field]; found {
			t.Fatalf("account export must not contain %s", field)
		}
	}

	protected, err := service.Export(context.Background(), ExportOptions{
		SiteIDs: []uint{firstSite.ID}, IncludeCredentials: true, Password: "portable-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(protected), "plain-password") || strings.Contains(string(protected), firstAccount.PasswordCipher) {
		t.Fatal("protected export leaked credential material")
	}
	protectedBundle, err := Decode(protected)
	if err != nil {
		t.Fatal(err)
	}
	aad, err := credentialAAD(protectedBundle)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := openCredentials("portable-secret", protectedBundle.Credentials, aad)
	if err != nil {
		t.Fatal(err)
	}
	accountKey := protectedBundle.Sites[0].Accounts[0].Key
	if credentials[accountKey].Value != "plain-password" {
		t.Fatalf("unexpected protected credential: %#v", credentials)
	}
	if _, err := service.Export(context.Background(), ExportOptions{SiteIDs: []uint{secondSite.ID + 100}}); err == nil {
		t.Fatal("expected invalid site error")
	}
}

func TestCredentialEnvelopeBindsBundleConfiguration(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "source-secret")
	site, _ := createBundleTestSite(t, db, cipher, "Bound", storage.UpstreamTypeNewAPI, "https://bound.example.com", "Primary", "plain-password")
	data, err := service.Export(context.Background(), ExportOptions{SiteIDs: []uint{site.ID}, IncludeCredentials: true, Password: "portable-secret"})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["sites"].([]any)[0].(map[string]any)["base_url"] = "https://tampered.example.com"
	tampered, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := parseBundle(tampered, "portable-secret"); err == nil {
		t.Fatal("expected authenticated credential envelope to reject altered site configuration")
	}
}

func TestPreviewAndImportUpsertPreservesCredentialsAndRelations(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "target-secret")
	site, primary := createBundleTestSite(t, db, cipher, "Demo", storage.UpstreamTypeNewAPI, "https://target.example.com", "Primary", "keep-me")
	captcha := &storage.CaptchaConfig{Name: "local-captcha", Type: storage.CaptchaCapSolver, Enabled: true}
	if err := db.Create(captcha).Error; err != nil {
		t.Fatal(err)
	}
	primary.CaptchaConfigID = &captcha.ID
	if err := storage.NewUpstreamAccounts(db).Update(primary); err != nil {
		t.Fatal(err)
	}
	extra := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: "LocalOnly", Username: "local", PasswordCipher: primary.PasswordCipher,
		CredentialMode: storage.CredentialModePassword, RechargeMultiplierMode: "divide", MonitorEnabled: true,
	}
	if err := storage.NewUpstreamAccounts(db).Create(extra); err != nil {
		t.Fatal(err)
	}

	data := encodeBundle(t, testImportBundle("https://target.example.com/"))
	plan, err := service.Preview(context.Background(), data, ImportOptions{Strategy: StrategyUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasConflicts || plan.Summary.Create != 1 || plan.Summary.Update < 2 || plan.Summary.Warnings < 2 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	var countBefore int64
	if err := db.Model(&storage.UpstreamAccount{}).Count(&countBefore).Error; err != nil {
		t.Fatal(err)
	}
	if countBefore != 2 {
		t.Fatalf("preview changed database, count = %d", countBefore)
	}

	result, err := service.Import(context.Background(), data, ImportOptions{Strategy: StrategyUpsert}, plan.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Create != 1 {
		t.Fatalf("unexpected import result: %#v", result)
	}
	accounts, err := storage.NewUpstreamSites(db).ListAccounts(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 3 {
		t.Fatalf("existing objects were deleted or account missing: %#v", accounts)
	}
	gotPrimary := findBundleAccount(t, db, site.ID, "Primary")
	gotBackup := findBundleAccount(t, db, site.ID, "Backup")
	if gotPrimary.PasswordCipher == "" || gotBackup.MonitorEnabled {
		t.Fatalf("unexpected account result: primary=%#v backup=%#v", gotPrimary, gotBackup)
	}
	plain, err := cipher.Decrypt(gotPrimary.PasswordCipher)
	if err != nil || plain != "keep-me" {
		t.Fatalf("existing credential was not preserved: %q, %v", plain, err)
	}
	var refreshed storage.UpstreamSite
	if err := db.First(&refreshed, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.DefaultAccountID != gotBackup.ID {
		t.Fatalf("default account = %d, want %d", refreshed.DefaultAccountID, gotBackup.ID)
	}

	repeated, err := service.Preview(context.Background(), data, ImportOptions{Strategy: StrategyUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.HasConflicts || repeated.Summary.Create != 0 || repeated.Summary.Update != 0 {
		t.Fatalf("repeated import must be idempotent: %#v", repeated)
	}
}

func TestBaseURLChangeRequiresConfirmationAndUsesSiteTransaction(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "target-secret")
	site, account := createBundleTestSite(t, db, cipher, "Demo", storage.UpstreamTypeNewAPI, "https://old.example.com", "Primary", "keep-me")
	if err := db.Create(&storage.AuthSession{AccountID: account.ID, UserID: "42"}).Error; err != nil {
		t.Fatal(err)
	}
	data := encodeBundle(t, testImportBundle("https://new.example.com/"))

	plan, err := service.Preview(context.Background(), data, ImportOptions{Strategy: StrategyUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasConflicts || !plan.RequiresBaseURLConfirmation || len(plan.BaseURLChanges) != 1 || plan.BaseURLChanges[0].AffectedAccountCount != 1 {
		t.Fatalf("unexpected unconfirmed plan: %#v", plan)
	}
	if _, err := service.Import(context.Background(), data, ImportOptions{Strategy: StrategyUpsert}, plan.Digest); !errors.Is(err, ErrImportConflict) {
		t.Fatalf("import error = %v, want ErrImportConflict", err)
	}

	confirmed := ImportOptions{Strategy: StrategyUpsert, ConfirmBaseURLChanges: true}
	confirmedPlan, err := service.Preview(context.Background(), data, confirmed)
	if err != nil {
		t.Fatal(err)
	}
	if confirmedPlan.HasConflicts || confirmedPlan.RequiresBaseURLConfirmation || confirmedPlan.Digest != plan.Digest {
		t.Fatalf("confirmed plan must retain the preflight digest and be importable: %#v", confirmedPlan)
	}
	if _, err := service.Import(context.Background(), data, confirmed, plan.Digest); err != nil {
		t.Fatal(err)
	}
	var updatedSite storage.UpstreamSite
	if err := db.First(&updatedSite, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedSite.BaseURL != "https://new.example.com" {
		t.Fatalf("base URL = %q", updatedSite.BaseURL)
	}
	updatedAccount := findBundleAccount(t, db, site.ID, "Primary")
	if updatedAccount.MonitorEnabled {
		t.Fatal("existing account monitoring must remain paused after an endpoint change")
	}
	var sessionCount int64
	if err := db.Model(&storage.AuthSession{}).Where("account_id = ?", account.ID).Count(&sessionCount).Error; err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("session count = %d, want 0", sessionCount)
	}
}

func TestCreateOnlyAndTypeConflict(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "target-secret")
	_, _ = createBundleTestSite(t, db, cipher, "Demo", storage.UpstreamTypeNewAPI, "https://existing.example.com", "Primary", "keep-me")
	data := encodeBundle(t, testImportBundle("https://incoming.example.com"))
	plan, err := service.Preview(context.Background(), data, ImportOptions{Strategy: StrategyCreateOnly})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasConflicts || plan.Summary.Skip == 0 || plan.Summary.Create == 0 {
		t.Fatalf("unexpected create-only plan: %#v", plan)
	}

	conflict := testImportBundle("https://existing.example.com")
	conflict.Sites[0].Type = storage.UpstreamTypeSub2API
	conflictPlan, err := service.Preview(context.Background(), encodeBundle(t, conflict), ImportOptions{Strategy: StrategyUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if !conflictPlan.HasConflicts {
		t.Fatalf("expected type conflict: %#v", conflictPlan)
	}
	if _, err := service.Import(context.Background(), encodeBundle(t, conflict), ImportOptions{Strategy: StrategyUpsert}, conflictPlan.Digest); !errors.Is(err, ErrImportConflict) {
		t.Fatalf("import error = %v, want ErrImportConflict", err)
	}
}

func TestImportRejectsStalePreviewAndRollsBack(t *testing.T) {
	service, db, cipher := newBundleTestService(t, "target-secret")
	site, _ := createBundleTestSite(t, db, cipher, "Demo", storage.UpstreamTypeNewAPI, "https://target.example.com", "Primary", "keep-me")
	data := encodeBundle(t, testImportBundle("https://target.example.com"))
	plan, err := service.Preview(context.Background(), data, ImportOptions{Strategy: StrategyUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.UpstreamSite{}).Where("id = ?", site.ID).Update("sort_order", 999).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(context.Background(), data, ImportOptions{Strategy: StrategyUpsert}, plan.Digest); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("import error = %v, want ErrPreviewStale", err)
	}

	emptyService, emptyDB, _ := newBundleTestService(t, "rollback-secret")
	rollbackData := encodeBundle(t, testImportBundle("https://rollback.example.com"))
	rollbackPlan, err := emptyService.Preview(context.Background(), rollbackData, ImportOptions{Strategy: StrategyCreateOnly})
	if err != nil {
		t.Fatal(err)
	}
	callbackName := fmt.Sprintf("fail-site-account-bundle-%d", time.Now().UnixNano())
	if err := emptyDB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*storage.UpstreamAccount); ok {
			tx.AddError(errors.New("forced account create failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer emptyDB.Callback().Create().Remove(callbackName)
	if _, err := emptyService.Import(context.Background(), rollbackData, ImportOptions{Strategy: StrategyCreateOnly}, rollbackPlan.Digest); err == nil {
		t.Fatal("expected forced import failure")
	}
	var siteCount, accountCount int64
	if err := emptyDB.Model(&storage.UpstreamSite{}).Count(&siteCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := emptyDB.Model(&storage.UpstreamAccount{}).Count(&accountCount).Error; err != nil {
		t.Fatal(err)
	}
	if siteCount != 0 || accountCount != 0 {
		t.Fatalf("failed import must roll back: sites=%d accounts=%d", siteCount, accountCount)
	}
}

func TestDecodeRejectsLegacySchemaAndAccountEndpointFields(t *testing.T) {
	bundle := testImportBundle("https://example.com/path///")
	decoded, err := Decode(encodeBundle(t, bundle))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Sites[0].BaseURL != "https://example.com/path" {
		t.Fatalf("base URL was not normalized: %q", decoded.Sites[0].BaseURL)
	}
	for _, invalidBaseURL := range []string{"https://example.com?", "https://example.com#", "https://user@example.com"} {
		invalid := testImportBundle(invalidBaseURL)
		if _, err := Decode(encodeBundle(t, invalid)); err == nil {
			t.Fatalf("expected invalid base URL %q to be rejected", invalidBaseURL)
		}
	}

	legacy := testImportBundle("https://example.com")
	legacy.Schema = legacyBundleSchema
	if _, err := Decode(encodeBundle(t, legacy)); err == nil || !strings.Contains(err.Error(), legacyBundleSchema) {
		t.Fatalf("legacy schema error = %v", err)
	}

	raw := map[string]any{}
	if err := json.Unmarshal(encodeBundle(t, testImportBundle("https://example.com")), &raw); err != nil {
		t.Fatal(err)
	}
	account := raw["sites"].([]any)[0].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	for _, field := range []string{"type", "site_url", "base_url"} {
		account[field] = "https://forbidden.example.com"
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(data); err == nil {
			t.Fatalf("expected account field %q to be rejected", field)
		}
		delete(account, field)
	}
}
