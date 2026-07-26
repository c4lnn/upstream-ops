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

type rechargeAccountServiceStub struct {
	*account.Service
	info               *connector.RechargeInfo
	launch             *connector.RechargeLaunch
	subscriptionInfo   *connector.SubscriptionInfo
	subscriptionLaunch *connector.SubscriptionLaunch
	subscriptionUsage  *connector.SubscriptionUsageInfo
	subscriptionReq    connector.SubscriptionRequest
	calledAccountIDs   []uint
}

func (s *rechargeAccountServiceStub) record(accountID uint) {
	s.calledAccountIDs = append(s.calledAccountIDs, accountID)
}
func (s *rechargeAccountServiceStub) GetRechargeInfo(_ context.Context, accountID uint) (*connector.RechargeInfo, error) {
	s.record(accountID)
	return s.info, nil
}
func (s *rechargeAccountServiceStub) CreateRecharge(_ context.Context, accountID uint, _ connector.RechargeRequest) (*connector.RechargeLaunch, error) {
	s.record(accountID)
	return s.launch, nil
}
func (s *rechargeAccountServiceStub) RedeemCode(_ context.Context, accountID uint, _ string) (*connector.RedeemResult, error) {
	s.record(accountID)
	return &connector.RedeemResult{Message: "redeemed", Type: "balance", Value: 10}, nil
}
func (s *rechargeAccountServiceStub) GetSubscriptionInfo(_ context.Context, accountID uint) (*connector.SubscriptionInfo, error) {
	s.record(accountID)
	return s.subscriptionInfo, nil
}
func (s *rechargeAccountServiceStub) CreateSubscription(_ context.Context, accountID uint, req connector.SubscriptionRequest) (*connector.SubscriptionLaunch, error) {
	s.record(accountID)
	s.subscriptionReq = req
	return s.subscriptionLaunch, nil
}
func (s *rechargeAccountServiceStub) GetSubscriptionUsage(_ context.Context, accountID uint) (*connector.SubscriptionUsageInfo, error) {
	s.record(accountID)
	return s.subscriptionUsage, nil
}

func TestAccountRechargeEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openTestDB(t)
	sites := storage.NewUpstreamSites(db)
	accounts := storage.NewUpstreamAccounts(db)
	site := createTestSite(t, sites, "source", storage.UpstreamTypeSub2API, "https://source.example.com")
	first := createTestAccount(t, sites, site.ID, "primary")
	_ = createTestAccount(t, sites, site.ID, "backup")

	stub := &rechargeAccountServiceStub{
		info:               &connector.RechargeInfo{AmountLabel: "amount", AmountStep: 0.01, MinAmount: 5, Methods: []connector.RechargeMethod{{Type: "alipay", Name: "Alipay", MinAmount: 5}}},
		launch:             &connector.RechargeLaunch{Mode: "redirect", PayURL: "https://pay.example.com/go"},
		subscriptionInfo:   &connector.SubscriptionInfo{Plans: []connector.SubscriptionPlan{{ID: "7", Name: "Pro", Price: 29}}, Methods: []connector.SubscriptionMethod{{Type: "alipay", Name: "Alipay"}}},
		subscriptionLaunch: &connector.SubscriptionLaunch{Mode: "redirect", PayURL: "https://pay.example.com/sub"},
		subscriptionUsage:  &connector.SubscriptionUsageInfo{Items: []connector.SubscriptionUsage{{ID: 3, GroupName: "pro", Daily: &connector.SubscriptionUsageWindow{LimitUSD: 10, UsedUSD: 8, RemainingUSD: 2, RemainingPercent: 20}}}},
	}
	r := gin.New()
	registerAccounts(r.Group("/api"), &Deps{Accounts: accounts, Sites: sites, AccountSvc: stub})

	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/accounts/1/recharge-info", ""},
		{http.MethodPost, "/api/accounts/1/recharge", `{"amount":12.5,"payment_method":"alipay","is_mobile":true}`},
		{http.MethodPost, "/api/accounts/1/redeem", `{"code":"redeem-code"}`},
		{http.MethodGet, "/api/accounts/1/subscription-info", ""},
		{http.MethodPost, "/api/accounts/1/subscription", `{"plan_id":"7","payment_method":"alipay","is_mobile":true}`},
		{http.MethodGet, "/api/accounts/1/subscription-usage", ""},
	}
	for _, testCase := range requests {
		request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
		if testCase.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d body = %s", testCase.method, testCase.path, response.Code, response.Body.String())
		}
	}
	if stub.subscriptionReq.PlanID != "7" || stub.subscriptionReq.PaymentMethod != "alipay" || !stub.subscriptionReq.IsMobile {
		t.Fatalf("subscription request = %#v", stub.subscriptionReq)
	}
	for _, accountID := range stub.calledAccountIDs {
		if accountID != first.ID {
			t.Fatalf("account operation used account %d, want %d", accountID, first.ID)
		}
	}

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/accounts/1/recharge-info", nil))
	var payload struct {
		Data connector.RechargeInfo `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode recharge info: %v", err)
	}
	if payload.Data.AmountLabel != "amount" {
		t.Fatalf("recharge info = %#v", payload.Data)
	}
}
