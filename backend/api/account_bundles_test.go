package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/accountbundle"
	appcrypto "github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSiteAccountBundleAPITest(t *testing.T, protected bool) (*gin.Engine, *gorm.DB, *accountbundle.Service, *appcrypto.Cipher) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:site-account-bundle-api-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	cipher, err := appcrypto.NewCipher("api-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	service := accountbundle.NewService(db, cipher)
	router := gin.New()
	group := router.Group("/api")
	if protected {
		group.Use(func(c *gin.Context) {
			if c.GetHeader("Authorization") != "Bearer test-token" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
			c.Next()
		})
	}
	registerSiteAccountBundles(group, &Deps{AccountBundles: service})
	return router, db, service, cipher
}

func createSiteAccountBundleAPIAccount(t *testing.T, db *gorm.DB, cipher *appcrypto.Cipher) (*storage.UpstreamSite, *storage.UpstreamAccount) {
	t.Helper()
	site := &storage.UpstreamSite{Name: "API Site", Type: storage.UpstreamTypeNewAPI, BaseURL: "https://api.example.com", SortOrder: 1}
	if err := storage.NewUpstreamSites(db).Create(site); err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("api-password")
	if err != nil {
		t.Fatal(err)
	}
	account := &storage.UpstreamAccount{
		SiteID: site.ID, Alias: "Primary", Username: "api-user", PasswordCipher: encrypted,
		CredentialMode: storage.CredentialModePassword, RechargeMultiplierMode: "divide", MonitorEnabled: true,
	}
	if err := storage.NewUpstreamAccounts(db).Create(account); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewUpstreamSites(db).SetDefaultAccount(site.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	return site, account
}

func multipartBundleRequest(t *testing.T, method, path string, data []byte, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "site-accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func apiImportBundle(baseURL string) accountbundle.Bundle {
	return accountbundle.Bundle{
		Schema: accountbundle.BundleSchema, Version: accountbundle.BundleVersion, ExportedAt: time.Now().UTC(),
		Sites: []accountbundle.SiteConfig{{
			Key: "site-1", Name: "Imported", Type: storage.UpstreamTypeNewAPI, BaseURL: baseURL, SortOrder: 1,
			DefaultAccount: "account-1",
			Accounts: []accountbundle.AccountConfig{{
				Key: "account-1", Alias: "Primary", AccountSortOrder: 1,
				CredentialMode: storage.CredentialModePassword, RechargeMultiplierMode: "divide", MonitorEnabled: true,
			}},
		}},
	}
}

func TestSiteAccountBundleRoutesInheritAuthenticationAndDownload(t *testing.T) {
	router, db, _, cipher := newSiteAccountBundleAPITest(t, true)
	site, _ := createSiteAccountBundleAPIAccount(t, db, cipher)
	payload, _ := json.Marshal(siteAccountBundleExportInput{SiteIDs: []uint{site.ID}})

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/site-account-bundles/export", bytes.NewReader(payload)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/site-account-bundles/export", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(response.Header().Get("Content-Disposition"), "site-accounts") || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unexpected download headers: %#v", response.Header())
	}
	if strings.Contains(response.Body.String(), "api-password") {
		t.Fatal("download leaked password")
	}
}

func TestSiteAccountBundlePreviewImportAndTypeConflict(t *testing.T) {
	router, db, _, _ := newSiteAccountBundleAPITest(t, false)
	bundle := apiImportBundle("https://import.example.com")
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	previewResponse := httptest.NewRecorder()
	router.ServeHTTP(previewResponse, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", data, map[string]string{"strategy": "create_only"}))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Data accountbundle.ImportPlan `json:"data"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Data.Digest == "" || preview.Data.Summary.Create != 2 || preview.Data.HasConflicts {
		t.Fatalf("unexpected preview: %#v", preview.Data)
	}

	importResponse := httptest.NewRecorder()
	router.ServeHTTP(importResponse, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import", data, map[string]string{
		"strategy": "create_only", "digest": preview.Data.Digest,
	}))
	if importResponse.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", importResponse.Code, importResponse.Body.String())
	}
	var siteCount, accountCount int64
	if err := db.Model(&storage.UpstreamSite{}).Count(&siteCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.UpstreamAccount{}).Count(&accountCount).Error; err != nil {
		t.Fatal(err)
	}
	if siteCount != 1 || accountCount != 1 {
		t.Fatalf("siteCount=%d accountCount=%d", siteCount, accountCount)
	}

	conflict := apiImportBundle("https://import.example.com")
	conflict.Sites[0].Type = storage.UpstreamTypeSub2API
	conflictData, _ := json.Marshal(conflict)
	conflictPreview := httptest.NewRecorder()
	router.ServeHTTP(conflictPreview, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", conflictData, map[string]string{"strategy": "upsert"}))
	if conflictPreview.Code != http.StatusOK {
		t.Fatalf("conflict preview status=%d body=%s", conflictPreview.Code, conflictPreview.Body.String())
	}
	if err := json.Unmarshal(conflictPreview.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Data.HasConflicts {
		t.Fatal("expected blocking conflict")
	}
	conflictImport := httptest.NewRecorder()
	router.ServeHTTP(conflictImport, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import", conflictData, map[string]string{
		"strategy": "upsert", "digest": preview.Data.Digest,
	}))
	if conflictImport.Code != http.StatusConflict {
		t.Fatalf("conflict import status=%d body=%s", conflictImport.Code, conflictImport.Body.String())
	}
}

func TestSiteAccountBundleBaseURLConfirmation(t *testing.T) {
	router, db, _, cipher := newSiteAccountBundleAPITest(t, false)
	site, account := createSiteAccountBundleAPIAccount(t, db, cipher)
	if err := db.Create(&storage.AuthSession{AccountID: account.ID}).Error; err != nil {
		t.Fatal(err)
	}
	bundle := apiImportBundle("https://changed.example.com")
	bundle.Sites[0].Name = site.Name
	bundle.Sites[0].Accounts[0].Alias = account.Alias
	data, _ := json.Marshal(bundle)

	previewResponse := httptest.NewRecorder()
	router.ServeHTTP(previewResponse, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", data, map[string]string{"strategy": "upsert"}))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Data accountbundle.ImportPlan `json:"data"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Data.HasConflicts || !preview.Data.RequiresBaseURLConfirmation || len(preview.Data.BaseURLChanges) != 1 {
		t.Fatalf("unexpected unconfirmed preview: %#v", preview.Data)
	}
	unconfirmedDigest := preview.Data.Digest

	confirmedPreview := httptest.NewRecorder()
	router.ServeHTTP(confirmedPreview, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", data, map[string]string{
		"strategy": "upsert", "confirm_base_url_changes": "true",
	}))
	if confirmedPreview.Code != http.StatusOK {
		t.Fatalf("confirmed preview status=%d body=%s", confirmedPreview.Code, confirmedPreview.Body.String())
	}
	if err := json.Unmarshal(confirmedPreview.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Data.HasConflicts || preview.Data.RequiresBaseURLConfirmation || preview.Data.Digest != unconfirmedDigest {
		t.Fatalf("unexpected confirmed preview: %#v", preview.Data)
	}

	commit := httptest.NewRecorder()
	router.ServeHTTP(commit, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import", data, map[string]string{
		"strategy": "upsert", "digest": unconfirmedDigest, "confirm_base_url_changes": "true",
	}))
	if commit.Code != http.StatusOK {
		t.Fatalf("commit status=%d body=%s", commit.Code, commit.Body.String())
	}
	var refreshed storage.UpstreamSite
	if err := db.First(&refreshed, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.BaseURL != "https://changed.example.com" {
		t.Fatalf("base URL = %q", refreshed.BaseURL)
	}
}

func TestSiteAccountBundleAPIErrors(t *testing.T) {
	router, db, service, cipher := newSiteAccountBundleAPITest(t, false)
	site, _ := createSiteAccountBundleAPIAccount(t, db, cipher)
	protected, err := service.Export(t.Context(), accountbundle.ExportOptions{
		SiteIDs: []uint{site.ID}, IncludeCredentials: true, Password: "correct-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongPassword := httptest.NewRecorder()
	router.ServeHTTP(wrongPassword, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", protected, map[string]string{
		"strategy": "create_only", "password": "wrong-password",
	}))
	if wrongPassword.Code != http.StatusBadRequest {
		t.Fatalf("wrong password status=%d body=%s", wrongPassword.Code, wrongPassword.Body.String())
	}

	legacy := apiImportBundle("https://legacy.example.com")
	legacy.Schema = "upstream-ops/channel-bundle"
	legacyData, _ := json.Marshal(legacy)
	legacyResponse := httptest.NewRecorder()
	router.ServeHTTP(legacyResponse, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", legacyData, nil))
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy status=%d body=%s", legacyResponse.Code, legacyResponse.Body.String())
	}

	invalidConfirm := httptest.NewRecorder()
	router.ServeHTTP(invalidConfirm, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", protected, map[string]string{
		"confirm_base_url_changes": "not-a-bool",
	}))
	if invalidConfirm.Code != http.StatusBadRequest {
		t.Fatalf("invalid confirmation status=%d body=%s", invalidConfirm.Code, invalidConfirm.Body.String())
	}

	oversized := bytes.Repeat([]byte("x"), accountbundle.MaxBundleSize+1)
	oversizedResponse := httptest.NewRecorder()
	router.ServeHTTP(oversizedResponse, multipartBundleRequest(t, http.MethodPost, "/api/site-account-bundles/import/preview", oversized, nil))
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", oversizedResponse.Code, oversizedResponse.Body.String())
	}
}
