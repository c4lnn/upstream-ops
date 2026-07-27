package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/monitor"
	"github.com/bejix/upstream-ops/backend/progress"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

func registerAccounts(g *gin.RouterGroup, d *Deps) {
	gp := g.Group("/accounts")
	gp.GET("", func(c *gin.Context) { listAccounts(c, d) })
	gp.POST("/sync-all", func(c *gin.Context) { syncAllAccounts(c, d) })
	gp.GET("/:account_id", func(c *gin.Context) { getAccount(c, d) })
	gp.PUT("/:account_id", func(c *gin.Context) { updateAccount(c, d) })
	gp.DELETE("/:account_id", func(c *gin.Context) { deleteAccount(c, d) })
	gp.POST("/:account_id/clear-login-info", func(c *gin.Context) { clearAccountLoginInfo(c, d) })
	gp.POST("/:account_id/enable", func(c *gin.Context) { toggleAccount(c, d, true) })
	gp.POST("/:account_id/disable", func(c *gin.Context) { toggleAccount(c, d, false) })
	gp.POST("/:account_id/test-login", func(c *gin.Context) { testLogin(c, d) })
	gp.POST("/:account_id/refresh-balance", func(c *gin.Context) { refreshBalance(c, d) })
	gp.POST("/:account_id/refresh-rates", func(c *gin.Context) { refreshRates(c, d) })
	gp.POST("/:account_id/redeem", func(c *gin.Context) { redeemAccount(c, d) })
	gp.GET("/:account_id/recharge-info", func(c *gin.Context) { accountRechargeInfo(c, d) })
	gp.POST("/:account_id/recharge", func(c *gin.Context) { createAccountRecharge(c, d) })
	gp.GET("/:account_id/subscription-info", func(c *gin.Context) { accountSubscriptionInfo(c, d) })
	gp.POST("/:account_id/subscription", func(c *gin.Context) { createAccountSubscription(c, d) })
	gp.GET("/:account_id/subscription-usage", func(c *gin.Context) { accountSubscriptionUsage(c, d) })
	gp.GET("/:account_id/api-keys/groups", func(c *gin.Context) { listAccountAPIKeyGroups(c, d) })
	gp.GET("/:account_id/api-keys", func(c *gin.Context) { listAccountAPIKeys(c, d) })
	gp.POST("/:account_id/api-keys", func(c *gin.Context) { createAccountAPIKey(c, d) })
	gp.PUT("/:account_id/api-keys/:key_id", func(c *gin.Context) { updateAccountAPIKey(c, d) })
	gp.DELETE("/:account_id/api-keys/:key_id", func(c *gin.Context) { deleteAccountAPIKey(c, d) })
	gp.POST("/:account_id/api-keys/:key_id/reveal", func(c *gin.Context) { revealAccountAPIKey(c, d) })
	gp.POST("/:account_id/sync", func(c *gin.Context) { syncAccount(c, d) })
	gp.GET("/:account_id/rates", func(c *gin.Context) { accountRates(c, d) })
	gp.GET("/:account_id/balance-history", func(c *gin.Context) { balanceHistory(c, d) })
}

type accountInput struct {
	Alias                  string                 `json:"alias" binding:"required"`
	Username               string                 `json:"username"`
	SortOrder              int                    `json:"sort_order"`
	Password               string                 `json:"password"`
	CredentialMode         storage.CredentialMode `json:"credential_mode"`
	TokenCredential        string                 `json:"token_credential"`
	LoginExtraParams       string                 `json:"login_extra_params"`
	TurnstileEnabled       bool                   `json:"turnstile_enabled"`
	SubscriptionEnabled    bool                   `json:"subscription_enabled"`
	ProxyEnabled           bool                   `json:"proxy_enabled"`
	CaptchaConfigID        *uint                  `json:"captcha_config_id"`
	BalanceThreshold       float64                `json:"balance_threshold"`
	RechargeMultiplier     *float64               `json:"recharge_multiplier"`
	RechargeMultiplierMode string                 `json:"recharge_multiplier_mode"`
	MonitorEnabled         *bool                  `json:"monitor_enabled"`
}

type accountUpdateInput struct {
	Alias                  *string                 `json:"alias"`
	Username               *string                 `json:"username"`
	SortOrder              *int                    `json:"sort_order"`
	Password               *string                 `json:"password"`
	CredentialMode         *storage.CredentialMode `json:"credential_mode"`
	TokenCredential        *string                 `json:"token_credential"`
	LoginExtraParams       *string                 `json:"login_extra_params"`
	TurnstileEnabled       *bool                   `json:"turnstile_enabled"`
	SubscriptionEnabled    *bool                   `json:"subscription_enabled"`
	ProxyEnabled           *bool                   `json:"proxy_enabled"`
	CaptchaConfigID        *uint                   `json:"captcha_config_id"`
	BalanceThreshold       *float64                `json:"balance_threshold"`
	RechargeMultiplier     *float64                `json:"recharge_multiplier"`
	RechargeMultiplierMode *string                 `json:"recharge_multiplier_mode"`
	MonitorEnabled         *bool                   `json:"monitor_enabled"`
}

type accountOutput struct {
	storage.UpstreamAccount
	UserID string `json:"user_id,omitempty"`
}

type accountRedeemInput struct {
	Code string `json:"code"`
}

type accountRechargeInput struct {
	Amount        float64 `json:"amount"`
	PaymentMethod string  `json:"payment_method"`
	IsMobile      bool    `json:"is_mobile"`
}

type accountSubscriptionInput struct {
	PlanID        string `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
	IsMobile      bool   `json:"is_mobile"`
}

type accountAPIKeyCreateInput = connector.APIKeyCreateRequest
type accountAPIKeyUpdateInput = connector.APIKeyUpdateRequest

var forbiddenAccountFields = []string{"type", "base_url", "site_url", "site_id", "upstream_site_id"}

// bindAccountJSON prevents an account from overriding the connection target
// owned by its upstream site.
func bindAccountJSON(c *gin.Context, target any) error {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindBodyWith(&raw, binding.JSON); err != nil {
		return err
	}
	for _, field := range forbiddenAccountFields {
		if _, ok := raw[field]; ok {
			return fmt.Errorf("账号请求不允许字段 %q", field)
		}
	}
	return c.ShouldBindBodyWith(target, binding.JSON)
}

func listAccounts(c *gin.Context, d *Deps) {
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page, pageSize, err := parseAccountPageQuery(c)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		list, total, err := d.Accounts.ListPage(page, pageSize)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		pages := 1
		if total > 0 && pageSize != -1 {
			pages = int((total + int64(pageSize) - 1) / int64(pageSize))
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items":     accountOutputs(d, list),
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"pages":     pages,
		}})
		return
	}

	list, err := d.Accounts.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accountOutputs(d, list)})
}

func createAccountForSite(d *Deps, siteID uint, in accountInput) (*storage.UpstreamAccount, error) {
	return d.AccountSvc.Create(account.CreateInput{
		SiteID:                 siteID,
		Alias:                  in.Alias,
		Username:               in.Username,
		AccountSortOrder:       in.SortOrder,
		Password:               in.Password,
		CredentialMode:         in.CredentialMode,
		TokenCredential:        in.TokenCredential,
		LoginExtraParams:       in.LoginExtraParams,
		TurnstileEnabled:       in.TurnstileEnabled,
		SubscriptionEnabled:    in.SubscriptionEnabled,
		ProxyEnabled:           in.ProxyEnabled,
		CaptchaConfigID:        in.CaptchaConfigID,
		BalanceThreshold:       in.BalanceThreshold,
		RechargeMultiplier:     in.RechargeMultiplier,
		RechargeMultiplierMode: in.RechargeMultiplierMode,
		MonitorEnabled:         boolDefaultTrue(in.MonitorEnabled),
	})
}

func boolDefaultTrue(value *bool) bool {
	return value == nil || *value
}

func accountOutputs(d *Deps, list []storage.UpstreamAccount) []accountOutput {
	out := make([]accountOutput, 0, len(list))
	for _, ch := range list {
		out = append(out, accountOutputFor(d, ch))
	}
	return out
}

func accountOutputFor(d *Deps, ch storage.UpstreamAccount) accountOutput {
	out := accountOutput{UpstreamAccount: ch}
	out.UserID = accountUserID(d, &ch)
	return out
}

func accountUserID(d *Deps, ch *storage.UpstreamAccount) string {
	if d == nil || d.Sites == nil || ch == nil {
		return ""
	}
	site, err := d.Sites.FindByID(ch.SiteID)
	if err != nil || site.Type != storage.UpstreamTypeNewAPI {
		return ""
	}
	if ch.CredentialMode == storage.CredentialModeToken && d.Cipher != nil && ch.PasswordCipher != "" {
		raw, err := d.Cipher.Decrypt(ch.PasswordCipher)
		if err == nil {
			var cred account.NewAPITokenCredential
			if json.Unmarshal([]byte(raw), &cred) == nil {
				if userID := strings.TrimSpace(cred.UserID); userID != "" {
					return userID
				}
			}
		}
	}
	if d.Sessions != nil {
		session, err := d.Sessions.FindByAccount(ch.ID)
		if err == nil && session != nil {
			return strings.TrimSpace(session.UserID)
		}
	}
	return ""
}

func getAccount(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Accounts.FindByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accountOutputFor(d, *ch)})
}

func updateAccount(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in accountUpdateInput
	if err := bindAccountJSON(c, &in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	updated, err := d.AccountSvc.Update(id, account.UpdateInput{
		Alias:                  in.Alias,
		Username:               in.Username,
		AccountSortOrder:       in.SortOrder,
		Password:               in.Password,
		CredentialMode:         in.CredentialMode,
		TokenCredential:        in.TokenCredential,
		LoginExtraParams:       in.LoginExtraParams,
		TurnstileEnabled:       in.TurnstileEnabled,
		SubscriptionEnabled:    in.SubscriptionEnabled,
		ProxyEnabled:           in.ProxyEnabled,
		CaptchaConfigID:        in.CaptchaConfigID,
		BalanceThreshold:       in.BalanceThreshold,
		RechargeMultiplier:     in.RechargeMultiplier,
		RechargeMultiplierMode: in.RechargeMultiplierMode,
		MonitorEnabled:         in.MonitorEnabled,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accountOutputFor(d, *updated)})
}

func deleteAccount(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	replacementID, parseErr := optionalUintQuery(c, "replacement_account_id")
	if parseErr != nil {
		fail(c, http.StatusBadRequest, parseErr)
		return
	}
	var deleteErr error
	if d.Sites != nil {
		deleteErr = d.Sites.DeleteAccount(id, replacementID)
	} else {
		deleteErr = d.AccountSvc.Delete(id)
	}
	if deleteErr != nil {
		fail(c, http.StatusBadRequest, deleteErr)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func optionalUintQuery(c *gin.Context, name string) (uint, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return uint(value), nil
}

func clearAccountLoginInfo(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	updated, err := d.AccountSvc.ClearLoginInfo(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": accountOutputFor(d, *updated)})
}

func toggleAccount(c *gin.Context, d *Deps, enabled bool) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	_, err = d.AccountSvc.Update(id, account.UpdateInput{MonitorEnabled: &enabled})
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "monitor_enabled": enabled})
}

func testLogin(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Accounts.FindByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}

	obs := setupSSE(c)
	ctx := progress.WithObserver(c.Request.Context(), accountScopedObserver{
		base:         obs,
		accountID:    ch.ID,
		accountAlias: ch.Alias,
		index:        1,
		total:        1,
	})

	if err := d.AccountSvc.TestLogin(ctx, id); err != nil {
		progress.Fail(ctx, progress.StageError, err.Error())
		return
	}
	progress.OK(ctx, progress.StageDone, "登录测试成功")
}

func refreshBalance(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Accounts.FindByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	if err := d.Monitor.RefreshBalance(c.Request.Context(), ch); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func refreshRates(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Accounts.FindByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	if err := d.Monitor.RefreshRates(c.Request.Context(), ch); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func redeemAccount(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if _, err := d.Accounts.FindByID(id); err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}

	var in accountRedeemInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	res, err := d.AccountSvc.RedeemCode(c.Request.Context(), id, in.Code)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func accountRechargeInfo(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	info, err := d.AccountSvc.GetRechargeInfo(c.Request.Context(), id)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": info})
}

func createAccountRecharge(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in accountRechargeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if in.Amount <= 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("充值金额必须大于 0"))
		return
	}
	if in.PaymentMethod != "alipay" && in.PaymentMethod != "wxpay" {
		fail(c, http.StatusBadRequest, fmt.Errorf("仅支持 alipay 或 wxpay"))
		return
	}
	res, err := d.AccountSvc.CreateRecharge(c.Request.Context(), id, connector.RechargeRequest{
		Amount:        in.Amount,
		PaymentMethod: in.PaymentMethod,
		IsMobile:      in.IsMobile,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func accountSubscriptionInfo(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	info, err := d.AccountSvc.GetSubscriptionInfo(c.Request.Context(), id)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": info})
}

func createAccountSubscription(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in accountSubscriptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.PlanID) == "" {
		fail(c, http.StatusBadRequest, fmt.Errorf("请选择订阅套餐"))
		return
	}
	if strings.TrimSpace(in.PaymentMethod) == "" {
		fail(c, http.StatusBadRequest, fmt.Errorf("请选择支付方式"))
		return
	}
	res, err := d.AccountSvc.CreateSubscription(c.Request.Context(), id, connector.SubscriptionRequest{
		PlanID:        in.PlanID,
		PaymentMethod: in.PaymentMethod,
		IsMobile:      in.IsMobile,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func accountSubscriptionUsage(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	info, err := d.AccountSvc.GetSubscriptionUsage(c.Request.Context(), id)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": info})
}

func listAccountAPIKeys(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	page, pageSize, err := parsePageQuery(c)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	res, err := d.AccountSvc.ListAPIKeys(c.Request.Context(), id, connector.APIKeyQuery{
		Page:     page,
		PageSize: pageSize,
		Search:   c.Query("search"),
		Status:   c.Query("status"),
		GroupID:  c.Query("group_id"),
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func listAccountAPIKeyGroups(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	res, err := d.AccountSvc.ListAPIKeyGroups(c.Request.Context(), id)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func createAccountAPIKey(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	var in accountAPIKeyCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		fail(c, http.StatusBadRequest, fmt.Errorf("密钥名称不能为空"))
		return
	}
	res, err := d.AccountSvc.CreateAPIKey(c.Request.Context(), id, connector.APIKeyCreateRequest(in))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func updateAccountAPIKey(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	keyID, err := int64Param(c, "key_id")
	if err != nil || keyID <= 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("密钥 ID 无效"))
		return
	}
	var in accountAPIKeyUpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	res, err := d.AccountSvc.UpdateAPIKey(c.Request.Context(), id, keyID, connector.APIKeyUpdateRequest(in))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

func deleteAccountAPIKey(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	keyID, err := int64Param(c, "key_id")
	if err != nil || keyID <= 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("密钥 ID 无效"))
		return
	}
	if err := d.AccountSvc.DeleteAPIKey(c.Request.Context(), id, keyID); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func revealAccountAPIKey(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	keyID, err := int64Param(c, "key_id")
	if err != nil || keyID <= 0 {
		fail(c, http.StatusBadRequest, fmt.Errorf("密钥 ID 无效"))
		return
	}
	key, err := d.AccountSvc.RevealAPIKey(c.Request.Context(), id, keyID)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"key": key}})
}

func accountRates(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	list, err := d.Rates.ListByAccount(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func balanceHistory(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	list, err := d.Rates.BalanceHistory(id, limit)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": list})
}

func uintParam(c *gin.Context, name string) (uint, error) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	return uint(id), err
}

func int64Param(c *gin.Context, name string) (int64, error) {
	return strconv.ParseInt(c.Param(name), 10, 64)
}

func parsePageQuery(c *gin.Context) (int, int, error) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		return 0, 0, fmt.Errorf("page 必须是正整数")
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 {
		return 0, 0, fmt.Errorf("page_size 必须是正整数")
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize, nil
}

func parseAccountPageQuery(c *gin.Context) (int, int, error) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		return 0, 0, fmt.Errorf("page 必须是正整数")
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize == 0 || pageSize < -1 {
		return 0, 0, fmt.Errorf("page_size 必须是正整数或 -1")
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize, nil
}

// setupSSE 为 ResponseWriter 设置 text/event-stream 头，并返回一个就绪的 sseObserver。
// 调用方接下来一般是：
//
//	obs := setupSSE(c)
//	ctx := progress.WithObserver(c.Request.Context(), obs)
//	// ... 业务逻辑里的 progress.Start / OK / Fail 会被实时 stream 出去 ...
//	obs.Emit(progress.Event{Stage: progress.StageDone, Message: "完成"})
func setupSSE(c *gin.Context) *sseObserver {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // disable nginx-style proxy buffering
	c.Writer.WriteHeader(http.StatusOK)

	obs := &sseObserver{w: c.Writer}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		obs.flush = flusher.Flush
	}
	return obs
}

// sseObserver serializes progress events as SSE records.
type sseObserver struct {
	mu     sync.Mutex
	w      io.Writer
	flush  func()
	closed bool
}

func (o *sseObserver) Emit(ev progress.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	// SSE: "data: <json>\n\n"
	if _, err := io.WriteString(o.w, "data: "); err != nil {
		o.closed = true
		return
	}
	if _, err := o.w.Write(payload); err != nil {
		o.closed = true
		return
	}
	if _, err := io.WriteString(o.w, "\n\n"); err != nil {
		o.closed = true
		return
	}
	if o.flush != nil {
		o.flush()
	}
}

type accountScopedObserver struct {
	base         progress.Observer
	accountID    uint
	accountAlias string
	index        int
	total        int
}

func (o accountScopedObserver) Emit(ev progress.Event) {
	ev.AccountID = o.accountID
	ev.AccountAlias = o.accountAlias
	ev.Index = o.index
	ev.Total = o.total
	o.base.Emit(ev)
}

// syncAccount streams one account's refresh progress.
func syncAccount(c *gin.Context, d *Deps) {
	id, err := uintParam(c, "account_id")
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ch, err := d.Accounts.FindByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}

	obs := setupSSE(c)
	if service, ok := d.Monitor.(interface {
		SyncAccount(context.Context, uint) ([]monitor.SiteAccountSyncResult, error)
	}); ok {
		ctx := progress.WithObserver(c.Request.Context(), obs)
		items, syncErr := service.SyncAccount(ctx, id)
		emitSyncSummary(obs, monitor.NewSyncSummary(items, syncErr))
		return
	}
	ctx := progress.WithObserver(c.Request.Context(), accountScopedObserver{
		base:         obs,
		accountID:    ch.ID,
		accountAlias: ch.Alias,
		index:        1,
		total:        1,
	})

	balErr := d.Monitor.RefreshBalance(ctx, ch)
	var subErr error
	if balErr == nil {
		subErr = d.Monitor.CheckSubscriptionUsageAlerts(ctx, ch)
	}
	rateErr := d.Monitor.RefreshRates(ctx, ch)

	switch {
	case balErr != nil || subErr != nil || rateErr != nil:
		progress.Fail(ctx, progress.StageError, joinErrorMessages(balErr, subErr, rateErr))
	default:
		progress.OK(ctx, progress.StageDone, "同步完成")
	}
}

func syncAllAccounts(c *gin.Context, d *Deps) {
	if service, ok := d.Monitor.(interface {
		SyncAll(context.Context) ([]monitor.SiteAccountSyncResult, error)
	}); ok {
		obs := setupSSE(c)
		ctx := progress.WithObserver(c.Request.Context(), obs)
		items, syncErr := service.SyncAll(ctx)
		emitSyncSummary(obs, monitor.NewSyncSummary(items, syncErr))
		return
	}
	list, err := d.Accounts.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}

	obs := setupSSE(c)
	baseCtx := c.Request.Context()
	total := len(list)
	var successCount, failedCount int

	if total == 0 {
		obs.Emit(progress.Event{
			Stage:   progress.StageDone,
			Message: "批量同步完成：成功 0，失败 0",
			Time:    time.Now(),
		})
		return
	}

	for i := range list {
		ch := list[i]
		scoped := accountScopedObserver{
			base:         obs,
			accountID:    ch.ID,
			accountAlias: ch.Alias,
			index:        i + 1,
			total:        total,
		}
		ctx := progress.WithObserver(baseCtx, scoped)

		if err := d.Monitor.RefreshBalance(ctx, &ch); err != nil {
			failedCount++
			scoped.Emit(progress.Event{
				Stage:   progress.StageError,
				Message: fmt.Sprintf("同步失败: %v", err),
				Time:    time.Now(),
			})
			continue
		}
		if err := d.Monitor.CheckSubscriptionUsageAlerts(ctx, &ch); err != nil {
			failedCount++
			scoped.Emit(progress.Event{
				Stage:   progress.StageError,
				Message: fmt.Sprintf("订阅检查失败: %v", err),
				Time:    time.Now(),
			})
			continue
		}

		successCount++
		scoped.Emit(progress.Event{
			Stage:   progress.StageDone,
			Message: "同步完成",
			Time:    time.Now(),
		})
	}

	summary := fmt.Sprintf("批量同步完成: 成功 %d，失败 %d", successCount, failedCount)
	stage := progress.StageDone
	if failedCount > 0 {
		stage = progress.StageError
	}
	obs.Emit(progress.Event{
		Stage:   stage,
		Message: summary,
		Time:    time.Now(),
	})
}

func emitSyncSummary(obs progress.Observer, summary monitor.SyncSummary) {
	ok := summary.Status == "success"
	message := fmt.Sprintf("同步完成 · 成功 %d", summary.SuccessCount)
	if summary.Status == "partial" {
		message = fmt.Sprintf("部分同步完成 · 成功 %d / 失败 %d", summary.SuccessCount, summary.FailedCount)
	} else if summary.Status == "failed" {
		message = fmt.Sprintf("同步失败 · 失败 %d", summary.FailedCount)
	}
	obs.Emit(progress.Event{
		Stage: progress.StageDone, Message: message, OK: &ok, Data: summary,
		Time: time.Now(), Scope: "operation",
	})
}

func joinErrorMessages(errs ...error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, " | ")
}
