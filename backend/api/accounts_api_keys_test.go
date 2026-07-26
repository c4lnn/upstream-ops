package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

type apiKeyAccountServiceStub struct {
	*account.Service
	page          *connector.APIKeyPage
	groups        []connector.APIKeyGroup
	created       *connector.APIKey
	updated       *connector.APIKey
	revealed      string
	lastQuery     connector.APIKeyQuery
	lastAccountID uint
}

func (s *apiKeyAccountServiceStub) ListAPIKeys(_ context.Context, accountID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	s.lastAccountID = accountID
	s.lastQuery = query
	return s.page, nil
}

func (s *apiKeyAccountServiceStub) ListAPIKeyGroups(_ context.Context, accountID uint) ([]connector.APIKeyGroup, error) {
	s.lastAccountID = accountID
	return s.groups, nil
}

func (s *apiKeyAccountServiceStub) CreateAPIKey(_ context.Context, accountID uint, _ connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	s.lastAccountID = accountID
	return s.created, nil
}

func (s *apiKeyAccountServiceStub) UpdateAPIKey(_ context.Context, accountID uint, _ int64, _ connector.APIKeyUpdateRequest) (*connector.APIKey, error) {
	s.lastAccountID = accountID
	return s.updated, nil
}

func (s *apiKeyAccountServiceStub) DeleteAPIKey(_ context.Context, accountID uint, _ int64) error {
	s.lastAccountID = accountID
	return nil
}

func (s *apiKeyAccountServiceStub) RevealAPIKey(_ context.Context, accountID uint, _ int64) (string, error) {
	s.lastAccountID = accountID
	return s.revealed, nil
}

func TestAccountAPIKeyEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	site := createTestSite(t, sites, "source", storage.UpstreamTypeNewAPI, "https://source.example.com")
	first := createTestAccount(t, sites, site.ID, "primary")
	_ = createTestAccount(t, sites, site.ID, "backup")

	stub := &apiKeyAccountServiceStub{
		page:     &connector.APIKeyPage{Items: []connector.APIKey{{ID: 11, Name: "key1", Status: "active"}}, Total: 1, Page: 1, PageSize: 20, Pages: 1},
		groups:   []connector.APIKeyGroup{{Name: "default", Ratio: 1}},
		created:  &connector.APIKey{ID: 12, Name: "created", Status: "active"},
		updated:  &connector.APIKey{ID: 11, Name: "updated", Status: "disabled"},
		revealed: "sk-full",
	}
	r := gin.New()
	registerAccounts(r.Group("/api"), &Deps{Accounts: accounts, Sites: sites, AccountSvc: stub})

	req := httptest.NewRequest(http.MethodGet, "/api/accounts/1/api-keys?page=1&page_size=20&search=abc", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	if stub.lastAccountID != first.ID || stub.lastQuery.Search != "abc" {
		t.Fatalf("list used account/query = %d/%#v", stub.lastAccountID, stub.lastQuery)
	}
	var listResp struct {
		Data connector.APIKeyPage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Data.Items) != 1 || listResp.Data.Items[0].ID != 11 {
		t.Fatalf("list response = %#v", listResp.Data)
	}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/accounts/1/api-keys/groups", nil),
		httptest.NewRequest(http.MethodPost, "/api/accounts/1/api-keys", strings.NewReader(`{"name":"created"}`)),
		httptest.NewRequest(http.MethodPut, "/api/accounts/1/api-keys/11", strings.NewReader(`{"status":"disabled"}`)),
		httptest.NewRequest(http.MethodPost, "/api/accounts/1/api-keys/11/reveal", nil),
		httptest.NewRequest(http.MethodDelete, "/api/accounts/1/api-keys/11", nil),
	} {
		if request.Body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d body = %s", request.Method, request.URL.Path, response.Code, response.Body.String())
		}
	}
	if stub.lastAccountID != first.ID {
		t.Fatalf("API Key operation used account %d, want %d", stub.lastAccountID, first.ID)
	}

	oldRoute := httptest.NewRecorder()
	r.ServeHTTP(oldRoute, httptest.NewRequest(http.MethodGet, "/api/channels/1/api-keys", nil))
	if oldRoute.Code != http.StatusNotFound {
		t.Fatalf("old route status = %d", oldRoute.Code)
	}
}

func TestAccountAPIKeyValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAccounts(r.Group("/api"), &Deps{AccountSvc: &apiKeyAccountServiceStub{}})

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/accounts/1/api-keys?page=0", nil),
		httptest.NewRequest(http.MethodPost, "/api/accounts/1/api-keys", strings.NewReader(`{"name":" "}`)),
		httptest.NewRequest(http.MethodDelete, "/api/accounts/1/api-keys/0", nil),
	} {
		if request.Body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d body = %s", request.Method, request.URL.Path, response.Code, response.Body.String())
		}
	}
}
