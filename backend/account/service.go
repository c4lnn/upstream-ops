// Package channel 提供渠道领域服务：把存储层的加密字段解开成 connector.AccountTarget，
// 处理登录会话的复用与刷新、手动测试登录、手动刷新余额 / 倍率等。
package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bejix/upstream-ops/backend/captcha"
	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/progress"
	"github.com/bejix/upstream-ops/backend/storage"
)

// SessionRefreshThreshold 距离过期还有多久就提前刷新登录。
const SessionRefreshThreshold = 5 * time.Minute

// tokenSessionTTL token 模式下给用户提供的 access_token 一个兜底有效期。
// 真正失效检测靠 connector.CheckAuth；若凭据里有 refresh_token，会优先尝试刷新并回写。
const tokenSessionTTL = 365 * 24 * time.Hour

// Service 渠道领域服务。
type Service struct {
	Accounts     *storage.UpstreamAccounts
	Sites        *storage.UpstreamSites
	AuthSessions *storage.AuthSessions
	Captchas     *storage.Captchas
	Rates        *storage.Rates
	MonitorLogs  *storage.MonitorLogs
	Cipher       *crypto.Cipher

	mu          sync.RWMutex
	proxyConfig config.ProxyConfig
	upstream    config.UpstreamConfig
}

func (s *Service) SetSites(sites *storage.UpstreamSites) {
	s.Sites = sites
}

func NewService(
	accounts *storage.UpstreamAccounts,
	authSessions *storage.AuthSessions,
	captchas *storage.Captchas,
	rates *storage.Rates,
	monitorLogs *storage.MonitorLogs,
	cipher *crypto.Cipher,
) *Service {
	return &Service{
		Accounts:     accounts,
		AuthSessions: authSessions,
		Captchas:     captchas,
		Rates:        rates,
		MonitorLogs:  monitorLogs,
		Cipher:       cipher,
	}
}

func (s *Service) UpdateProxyConfig(cfg config.ProxyConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyConfig = cfg
}

func (s *Service) UpdateUpstreamConfig(cfg config.UpstreamConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstream = cfg.WithDefaults()
}

func (s *Service) proxyURL() (string, error) {
	s.mu.RLock()
	cfg := s.proxyConfig
	s.mu.RUnlock()
	return cfg.ActiveURL()
}

func (s *Service) upstreamConfig() config.UpstreamConfig {
	s.mu.RLock()
	cfg := s.upstream
	s.mu.RUnlock()
	return cfg.WithDefaults()
}

func applyProxy(conn connector.Connector, resolved *connector.AccountTarget) {
	if resolved == nil || strings.TrimSpace(resolved.ProxyURL) == "" {
		return
	}
	if setter, ok := conn.(connector.ProxySetter); ok {
		setter.SetProxy(resolved.ProxyURL)
	}
}

func (s *Service) ApplyProxy(conn connector.Connector, resolved *connector.AccountTarget) {
	applyProxy(conn, resolved)
}

func applyHTTPConfig(conn any, cfg config.UpstreamConfig) {
	if setter, ok := conn.(connector.HTTPConfigSetter); ok {
		cfg = cfg.WithDefaults()
		setter.SetHTTPConfig(connector.HTTPConfig{
			Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
			UserAgent: cfg.UserAgent,
		})
	}
}

func (s *Service) applyHTTPConfig(conn any) {
	applyHTTPConfig(conn, s.upstreamConfig())
}

func (s *Service) ApplyHTTPConfig(conn any) {
	s.applyHTTPConfig(conn)
}

// NewAPITokenCredential token 模式下 NewAPI 的凭据 JSON 结构。
//
// 两种鉴权方式二选一：
//   - Cookie：浏览器 DevTools 里拷出来的整条 Cookie 头（典型形如 session=xxxxx; ...）
//   - AccessToken：NewAPI「个人设置 / 生成的系统访问令牌」即 user.access_token（32 位字符串）
//     发给上游时走 Authorization 头而不是 Cookie 头，session 续期无关。
//
// UserID：上游账号 ID（NewAPI 个人设置页可见，作为 New-Api-User 请求头必填）
type NewAPITokenCredential struct {
	Cookie      string `json:"cookie,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	UserID      string `json:"user_id"`
}

// Sub2APITokenCredential token 模式下 Sub2API 的凭据。
type Sub2APITokenCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// CreateInput 新建渠道使用的明文输入。
//
// CredentialMode 决定字段语义：
//   - password: Password 必填；Username 为登录账号
//   - token:    TokenCredential 必填（已序列化为 JSON 字符串）；Username 仅作展示备注
type CreateInput struct {
	SiteID                 uint
	Alias                  string
	Username               string
	AccountSortOrder       int
	Password               string
	CredentialMode         storage.CredentialMode
	TokenCredential        string
	LoginExtraParams       string
	TurnstileEnabled       bool
	SubscriptionEnabled    bool
	ProxyEnabled           bool
	CaptchaConfigID        *uint
	BalanceThreshold       float64
	RechargeMultiplier     *float64
	RechargeMultiplierMode string
	MonitorEnabled         bool
}

func (s *Service) Create(in CreateInput) (*storage.UpstreamAccount, error) {
	if in.SiteID == 0 || s.Sites == nil {
		return nil, errors.New("账号必须创建在已有站点下")
	}
	site, err := s.Sites.FindByID(in.SiteID)
	if err != nil {
		return nil, err
	}
	mode := in.CredentialMode
	if mode == "" {
		mode = storage.CredentialModePassword
	}
	rawCredential, err := selectRawCredential(mode, in.Password, in.TokenCredential)
	if err != nil {
		return nil, err
	}
	if err := validateCredential(site.Type, mode, rawCredential); err != nil {
		return nil, err
	}
	loginExtra, err := normalizeLoginExtraParams(in.LoginExtraParams)
	if err != nil {
		return nil, err
	}
	ciphertext, err := s.Cipher.Encrypt(rawCredential)
	if err != nil {
		return nil, fmt.Errorf("encrypt credential: %w", err)
	}
	account := &storage.UpstreamAccount{
		SiteID:                 site.ID,
		Alias:                  strings.TrimSpace(in.Alias),
		Username:               in.Username,
		AccountSortOrder:       normalizeSortOrder(in.AccountSortOrder),
		PasswordCipher:         ciphertext,
		CredentialMode:         mode,
		LoginExtraParams:       loginExtra,
		TurnstileEnabled:       in.TurnstileEnabled && mode == storage.CredentialModePassword,
		SubscriptionEnabled:    site.Type == storage.UpstreamTypeSub2API && in.SubscriptionEnabled,
		ProxyEnabled:           in.ProxyEnabled,
		CaptchaConfigID:        in.CaptchaConfigID,
		BalanceThreshold:       in.BalanceThreshold,
		RechargeMultiplier:     normalizeRechargeMultiplier(in.RechargeMultiplier),
		RechargeMultiplierMode: connector.NormalizeRechargeMultiplierMode(in.RechargeMultiplierMode),
		MonitorEnabled:         in.MonitorEnabled,
	}
	if account.Alias == "" {
		return nil, errors.New("账号别名不能为空")
	}
	if mode == storage.CredentialModeToken {
		account.CaptchaConfigID = nil
	}
	if err := s.Sites.AddAccount(account); err != nil {
		return nil, err
	}
	return account, nil
}

type UpdateInput struct {
	Alias                  *string
	Username               *string
	AccountSortOrder       *int
	Password               *string
	CredentialMode         *storage.CredentialMode
	TokenCredential        *string
	LoginExtraParams       *string
	TurnstileEnabled       *bool
	SubscriptionEnabled    *bool
	ProxyEnabled           *bool
	CaptchaConfigID        *uint
	BalanceThreshold       *float64
	RechargeMultiplier     *float64
	RechargeMultiplierMode *string
	MonitorEnabled         *bool
}

func (s *Service) Update(id uint, in UpdateInput) (*storage.UpstreamAccount, error) {
	account, err := s.Accounts.FindByID(id)
	if err != nil {
		return nil, err
	}
	if s.Sites == nil {
		return nil, errors.New("站点服务未配置")
	}
	site, err := s.Sites.FindByID(account.SiteID)
	if err != nil {
		return nil, err
	}
	if in.Alias != nil {
		account.Alias = strings.TrimSpace(*in.Alias)
		if account.Alias == "" {
			return nil, errors.New("账号别名不能为空")
		}
	}
	if in.Username != nil {
		account.Username = *in.Username
	}
	if in.AccountSortOrder != nil {
		account.AccountSortOrder = normalizeSortOrder(*in.AccountSortOrder)
	}
	mode := account.CredentialMode
	if in.CredentialMode != nil && *in.CredentialMode != "" {
		mode = *in.CredentialMode
	}
	if mode == "" {
		mode = storage.CredentialModePassword
	}
	modeChanged := mode != account.CredentialMode
	var rawCredential string
	switch mode {
	case storage.CredentialModePassword:
		if in.Password != nil && *in.Password != "" {
			rawCredential = *in.Password
		} else if modeChanged {
			return nil, errors.New("切换到账号密码模式时必须填写密码")
		}
	case storage.CredentialModeToken:
		if in.TokenCredential != nil && *in.TokenCredential != "" {
			rawCredential = *in.TokenCredential
		} else if modeChanged {
			return nil, errors.New("切换到 token 模式时必须填写凭据")
		}
	default:
		return nil, fmt.Errorf("unknown credential mode: %s", mode)
	}
	if rawCredential != "" {
		if err := validateCredential(site.Type, mode, rawCredential); err != nil {
			return nil, err
		}
		ciphertext, err := s.Cipher.Encrypt(rawCredential)
		if err != nil {
			return nil, fmt.Errorf("encrypt credential: %w", err)
		}
		account.PasswordCipher = ciphertext
		account.CredentialMode = mode
		_ = s.AuthSessions.Delete(account.ID)
	} else if modeChanged {
		return nil, errors.New("凭据模式变更必须同时提供新凭据")
	}
	if in.LoginExtraParams != nil {
		value, err := normalizeLoginExtraParams(*in.LoginExtraParams)
		if err != nil {
			return nil, err
		}
		if value != account.LoginExtraParams {
			account.LoginExtraParams = value
			_ = s.AuthSessions.Delete(account.ID)
		}
	}
	if in.TurnstileEnabled != nil {
		account.TurnstileEnabled = *in.TurnstileEnabled && mode == storage.CredentialModePassword
	}
	if in.SubscriptionEnabled != nil {
		account.SubscriptionEnabled = site.Type == storage.UpstreamTypeSub2API && *in.SubscriptionEnabled
	}
	if in.ProxyEnabled != nil {
		account.ProxyEnabled = *in.ProxyEnabled
	}
	if in.CaptchaConfigID != nil {
		if mode == storage.CredentialModePassword {
			account.CaptchaConfigID = in.CaptchaConfigID
		} else {
			account.CaptchaConfigID = nil
		}
	} else if mode == storage.CredentialModeToken {
		account.CaptchaConfigID = nil
	}
	if in.BalanceThreshold != nil {
		account.BalanceThreshold = *in.BalanceThreshold
	}
	if in.RechargeMultiplier != nil {
		account.RechargeMultiplier = normalizeRechargeMultiplier(in.RechargeMultiplier)
	}
	if in.RechargeMultiplierMode != nil {
		account.RechargeMultiplierMode = connector.NormalizeRechargeMultiplierMode(*in.RechargeMultiplierMode)
	}
	if in.MonitorEnabled != nil {
		account.MonitorEnabled = *in.MonitorEnabled
	}
	if err := s.Accounts.Update(account); err != nil {
		return nil, err
	}
	return account, nil
}

func normalizeRechargeMultiplier(v *float64) *float64 {
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

func normalizeSortOrder(v int) int {
	if v == 0 {
		return 1
	}
	return v
}

func normalizeLoginExtraParams(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", fmt.Errorf("解析附加表单参数 JSON 失败：%w", err)
	}
	if obj == nil {
		return "", errors.New("附加表单参数必须是 JSON 对象")
	}
	return raw, nil
}

func parseLoginExtraParams(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, fmt.Errorf("解析附加表单参数 JSON 失败：%w", err)
	}
	if obj == nil {
		return nil, errors.New("附加表单参数必须是 JSON 对象")
	}
	return obj, nil
}

// selectRawCredential 在 Create 时根据 mode 决定要落库的明文凭据字符串。
func selectRawCredential(mode storage.CredentialMode, password, tokenCredential string) (string, error) {
	switch mode {
	case storage.CredentialModePassword:
		if password == "" {
			return "", errors.New("账号密码模式下密码不能为空")
		}
		return password, nil
	case storage.CredentialModeToken:
		if tokenCredential == "" {
			return "", errors.New("token 模式下必须提供凭据")
		}
		return tokenCredential, nil
	default:
		return "", fmt.Errorf("unknown credential mode: %s", mode)
	}
}

// validateCredential 在保存前对凭据做语法 / 必填字段校验，能尽早把无效输入挡在 connector 外。
//
// 注意：这里只做语法层校验，不做"凭据是否真的有效"的网络验证——
// 那个交给后续 TestLogin / 第一次同步去发现。
func validateCredential(channelType storage.UpstreamType, mode storage.CredentialMode, raw string) error {
	if mode != storage.CredentialModeToken {
		return nil
	}
	switch channelType {
	case storage.UpstreamTypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 NewAPI 凭据 JSON 失败：%w", err)
		}
		cookie := strings.TrimSpace(cred.Cookie)
		accessToken := strings.TrimSpace(cred.AccessToken)
		if cookie == "" && accessToken == "" {
			return errors.New("NewAPI token 模式需要 Cookie 或系统访问令牌（二选一）")
		}
		if cookie != "" && accessToken != "" {
			return errors.New("NewAPI token 模式 Cookie 与系统访问令牌只能二选一")
		}
		if strings.TrimSpace(cred.UserID) == "" {
			return errors.New("NewAPI token 模式需要 User ID（在 NewAPI 个人设置页查看）")
		}
	case storage.UpstreamTypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 Sub2API 凭据 JSON 失败：%w", err)
		}
		if strings.TrimSpace(cred.AccessToken) == "" {
			return errors.New("Sub2API token 模式需要 access_token")
		}
	default:
		return fmt.Errorf("unknown channel type: %s", channelType)
	}
	return nil
}

func (s *Service) Delete(id uint) error {
	if s.Sites != nil {
		return s.Sites.DeleteAccount(id, 0)
	}
	_ = s.AuthSessions.Delete(id)
	return s.Accounts.Delete(id)
}

// ClearLoginInfo 清空渠道当前保存的登录信息。
//
// password 模式：只删除登录后缓存的 AuthSession（access_token / refresh_token / cookie / csrf）。
// token 模式：同时清空用户直接保存的 token/cookie JSON，避免继续复用旧凭据。
func (s *Service) ClearLoginInfo(id uint) (*storage.UpstreamAccount, error) {
	c, err := s.Accounts.FindByID(id)
	if err != nil {
		return nil, err
	}
	if err := s.AuthSessions.Delete(c.ID); err != nil {
		return nil, err
	}
	if c.CredentialMode == storage.CredentialModeToken {
		c.PasswordCipher = ""
		c.LastError = ""
		if err := s.Accounts.Update(c); err != nil {
			return nil, err
		}
		return c, nil
	}
	c.LastError = ""
	if err := s.Accounts.SetLastError(c.ID, ""); err != nil {
		return nil, err
	}
	return c, nil
}

// AccountContext is the single resolved input for an account-scoped remote call.
type AccountContext struct {
	Site    *storage.UpstreamSite
	Account *storage.UpstreamAccount
	Target  *connector.AccountTarget
}

func (s *Service) ResolveContext(ctx context.Context, accountID uint) (*AccountContext, error) {
	account, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return nil, err
	}
	target, site, err := s.resolveTarget(ctx, account)
	if err != nil {
		return nil, err
	}
	return &AccountContext{Site: site, Account: account, Target: target}, nil
}

// Resolve keeps the account service boundary usable by callers that already
// loaded the account while resolving the authoritative site target internally.
func (s *Service) Resolve(ctx context.Context, account *storage.UpstreamAccount) (*connector.AccountTarget, error) {
	target, _, err := s.resolveTarget(ctx, account)
	return target, err
}

func (s *Service) resolveTarget(ctx context.Context, account *storage.UpstreamAccount) (*connector.AccountTarget, *storage.UpstreamSite, error) {
	_ = ctx
	if account == nil || account.SiteID == 0 || s.Sites == nil {
		return nil, nil, errors.New("账号缺少有效站点")
	}
	site, err := s.Sites.FindByID(account.SiteID)
	if err != nil {
		return nil, nil, err
	}
	raw, err := s.Cipher.Decrypt(account.PasswordCipher)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt credential: %w", err)
	}
	resolved := &connector.AccountTarget{
		AccountID:              account.ID,
		Alias:                  account.Alias,
		Type:                   connector.UpstreamType(site.Type),
		BaseURL:                site.BaseURL,
		Username:               account.Username,
		LoginExtraParams:       nil,
		TurnstileEnabled:       account.TurnstileEnabled,
		RechargeMultiplier:     account.RechargeMultiplier,
		RechargeMultiplierMode: connector.NormalizeRechargeMultiplierMode(account.RechargeMultiplierMode),
	}
	loginExtraParams, err := parseLoginExtraParams(account.LoginExtraParams)
	if err != nil {
		return nil, nil, err
	}
	resolved.LoginExtraParams = loginExtraParams
	if account.ProxyEnabled {
		proxyURL, err := s.proxyURL()
		if err != nil {
			return nil, nil, err
		}
		resolved.ProxyURL = proxyURL
	}
	if account.CredentialMode == storage.CredentialModeToken {
		// token 模式：raw 是 JSON，Password 留空避免被 connector 误用
		resolved.Password = ""
	} else {
		resolved.Password = raw
	}
	return resolved, site, nil
}

// buildSessionFromToken 在 token 模式下，把用户提供的凭据 JSON 解析成 AuthSession。
// 不发任何 HTTP 请求——失效检测留给 connector.CheckAuth + 后续 GetBalance / GetRates。
func (s *Service) buildSessionFromToken(account *storage.UpstreamAccount, upstreamType connector.UpstreamType) (*connector.AuthSession, error) {
	raw, err := s.Cipher.Decrypt(account.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("登录信息已清空，请重新编辑渠道填写凭据")
	}
	switch upstreamType {
	case connector.TypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse newapi token credential: %w", err)
		}
		return &connector.AuthSession{
			UserID:      cred.UserID,
			Cookie:      cred.Cookie,
			AccessToken: cred.AccessToken,
			ExpiresAt:   time.Now().Add(tokenSessionTTL),
		}, nil
	case connector.TypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse sub2api token credential: %w", err)
		}
		return &connector.AuthSession{
			AccessToken:  strings.TrimSpace(cred.AccessToken),
			RefreshToken: strings.TrimSpace(cred.RefreshToken),
			ExpiresAt:    time.Now().Add(tokenSessionTTL),
		}, nil
	default:
		return nil, fmt.Errorf("unknown upstream type: %s", upstreamType)
	}
}

// prepareTurnstile 在调用 conn.Login 之前求解 Turnstile token。
// 没启用 turnstile 或者上游 site 公开接口说"未开启 Turnstile"时是空操作。
func (s *Service) prepareTurnstile(
	ctx context.Context,
	c *storage.UpstreamAccount,
	resolved *connector.AccountTarget,
	conn connector.Connector,
) error {
	if !c.TurnstileEnabled || c.CaptchaConfigID == nil {
		return nil
	}
	progress.Start(ctx, progress.StageCaptcha, "求解 Turnstile…")
	siteKey, err := conn.GetTurnstileSiteKey(ctx, resolved)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("fetch turnstile site key: %w", err)
	}
	if siteKey == "" {
		progress.OK(ctx, progress.StageCaptcha, "上游未开启 Turnstile，跳过")
		return nil
	}
	token, err := s.solveCaptcha(ctx, *c.CaptchaConfigID, siteKey, resolved.BaseURL)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("solve captcha: %w", err)
	}
	resolved.TurnstileToken = token
	progress.OK(ctx, progress.StageCaptcha, "打码完成")
	return nil
}

func (s *Service) solveCaptcha(ctx context.Context, captchaID uint, siteKey, pageURL string) (string, error) {
	cfg, err := s.Captchas.FindByID(captchaID)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "", errors.New("captcha config disabled")
	}
	apiKey, err := s.Cipher.Decrypt(cfg.APIKeyCipher)
	if err != nil {
		return "", err
	}
	proxyURL := ""
	if cfg.ProxyEnabled {
		var proxyErr error
		proxyURL, proxyErr = s.proxyURL()
		if proxyErr != nil {
			return "", proxyErr
		}
	}
	provider, err := captcha.BuildWithProxy(cfg, apiKey, proxyURL)
	if err != nil {
		return "", err
	}
	return provider.SolveTurnstile(ctx, siteKey, pageURL)
}

// EnsureSession 优先复用未过期的 session，否则重新登录并加密回写。
//
// token 模式：
//   - 跳过 AuthSessions 表与 Login 调用
//   - 每次构造一个临时 AuthSession（基于用户提供的凭据）返回
//   - CheckAuth 用来发现 token 是否还有效；失效会在 last_error 显示
func (s *Service) EnsureSession(
	ctx context.Context,
	c *storage.UpstreamAccount,
	resolved *connector.AccountTarget,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	if c.CredentialMode == storage.CredentialModeToken {
		progress.Start(ctx, progress.StageSession, "使用用户提供的 token…")
		session, err := s.buildSessionFromToken(c, resolved.Type)
		if err != nil {
			progress.Fail(ctx, progress.StageSession, err.Error())
			_ = s.Accounts.SetLastError(c.ID, err.Error())
			return nil, err
		}
		// 走一次 CheckAuth 确认 token 仍有效。失败时如果有 refresh_token，先尝试刷新并回写。
		if err := conn.CheckAuth(ctx, resolved, session); err != nil {
			if refreshed, ok, refreshErr := s.refreshProvidedTokenSession(ctx, c, resolved, conn, session); refreshErr != nil {
				progress.Fail(ctx, progress.StageSession, refreshErr.Error())
				_ = s.Accounts.SetLastError(c.ID, refreshErr.Error())
				return nil, refreshErr
			} else if ok {
				return refreshed, nil
			}
			msg := "token 已失效，请重新粘贴凭据：" + err.Error()
			progress.Fail(ctx, progress.StageSession, msg)
			_ = s.Accounts.SetLastError(c.ID, msg)
			return nil, errors.New(msg)
		}
		_ = s.Accounts.SetLastError(c.ID, "")
		progress.OK(ctx, progress.StageSession, "token 有效，跳过登录")
		return session, nil
	}

	saved, err := s.AuthSessions.FindByAccount(c.ID)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		session, err := s.decryptSession(saved)
		if err != nil {
			return nil, err
		}
		if saved.ExpiresAt != nil && time.Until(*saved.ExpiresAt) > SessionRefreshThreshold {
			// 轻量校验现有 session，不通过则继续尝试 refresh_token / 重新登录。
			progress.Start(ctx, progress.StageSession, "校验已有会话…")
			if err := conn.CheckAuth(ctx, resolved, session); err == nil {
				progress.OK(ctx, progress.StageSession, "复用现有会话")
				return session, nil
			}
			progress.OK(ctx, progress.StageSession, "会话校验失败，尝试刷新")
		}
		if refreshed, ok, err := s.refreshStoredSession(ctx, c, resolved, conn, session); err != nil {
			return nil, err
		} else if ok {
			return refreshed, nil
		}
	}
	return s.login(ctx, c, resolved, conn)
}

func (s *Service) refreshStoredSession(
	ctx context.Context,
	c *storage.UpstreamAccount,
	resolved *connector.AccountTarget,
	conn connector.Connector,
	session *connector.AuthSession,
) (*connector.AuthSession, bool, error) {
	if strings.TrimSpace(session.RefreshToken) == "" {
		return nil, false, nil
	}
	progress.Start(ctx, progress.StageSession, "使用 refresh_token 刷新会话…")
	refreshed, err := refreshSession(ctx, resolved, conn, session)
	if err != nil {
		progress.OK(ctx, progress.StageSession, "刷新失败，重新登录")
		return nil, false, nil
	}
	if err := s.persistSession(c.ID, refreshed); err != nil {
		progress.Fail(ctx, progress.StageSession, err.Error())
		return nil, false, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	progress.OK(ctx, progress.StageSession, "会话刷新成功")
	return refreshed, true, nil
}

func (s *Service) refreshProvidedTokenSession(
	ctx context.Context,
	c *storage.UpstreamAccount,
	resolved *connector.AccountTarget,
	conn connector.Connector,
	session *connector.AuthSession,
) (*connector.AuthSession, bool, error) {
	if strings.TrimSpace(session.RefreshToken) == "" {
		return nil, false, nil
	}
	progress.Start(ctx, progress.StageSession, "使用 refresh_token 刷新 token…")
	refreshed, err := refreshSession(ctx, resolved, conn, session)
	if err != nil {
		return nil, false, err
	}
	if err := conn.CheckAuth(ctx, resolved, refreshed); err != nil {
		return nil, false, fmt.Errorf("刷新后的 token 校验失败：%w", err)
	}
	if err := s.persistTokenCredential(c, resolved.Type, refreshed); err != nil {
		return nil, false, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	progress.OK(ctx, progress.StageSession, "token 刷新成功")
	return refreshed, true, nil
}

func refreshSession(
	ctx context.Context,
	resolved *connector.AccountTarget,
	conn connector.Connector,
	session *connector.AuthSession,
) (*connector.AuthSession, error) {
	refresher, ok := conn.(connector.SessionRefresher)
	if !ok {
		return nil, errors.New("connector does not support refresh_token")
	}
	refreshed, err := refresher.RefreshSession(ctx, resolved, session)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(refreshed.AccessToken) == "" {
		return nil, errors.New("refresh token returned empty access_token")
	}
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = session.RefreshToken
	}
	return refreshed, nil
}

func (s *Service) persistTokenCredential(c *storage.UpstreamAccount, upstreamType connector.UpstreamType, session *connector.AuthSession) error {
	switch upstreamType {
	case connector.TypeSub2API:
		cred := Sub2APITokenCredential{
			AccessToken:  strings.TrimSpace(session.AccessToken),
			RefreshToken: strings.TrimSpace(session.RefreshToken),
		}
		if cred.AccessToken == "" {
			return errors.New("Sub2API token 模式需要 access_token")
		}
		raw, err := json.Marshal(cred)
		if err != nil {
			return fmt.Errorf("marshal sub2api token credential: %w", err)
		}
		enc, err := s.Cipher.Encrypt(string(raw))
		if err != nil {
			return fmt.Errorf("encrypt token credential: %w", err)
		}
		c.PasswordCipher = enc
		return s.Accounts.Update(c)
	default:
		return fmt.Errorf("%s token 模式不支持 refresh_token", upstreamType)
	}
}

func (s *Service) login(
	ctx context.Context,
	c *storage.UpstreamAccount,
	resolved *connector.AccountTarget,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	if err := s.prepareTurnstile(ctx, c, resolved, conn); err != nil {
		return nil, err
	}
	progress.Start(ctx, progress.StageLogin, "登录上游…")
	started := time.Now()
	session, err := conn.Login(ctx, resolved)
	if err == nil {
		progress.Start(ctx, progress.StageSession, "验证登录会话…")
		if checkErr := conn.CheckAuth(ctx, resolved, session); checkErr != nil {
			err = fmt.Errorf("登录后鉴权失败：%w", checkErr)
			progress.Fail(ctx, progress.StageSession, err.Error())
		} else {
			progress.OK(ctx, progress.StageSession, "登录会话有效")
		}
	}
	finished := time.Now()
	_ = s.MonitorLogs.Append(&storage.MonitorLog{
		AccountID:    c.ID,
		Job:          storage.MonitorJobLogin,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		_ = s.AuthSessions.Delete(c.ID)
		_ = s.Accounts.SetLastError(c.ID, err.Error())
		return nil, err
	}
	if err := s.persistSession(c.ID, session); err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	progress.OK(ctx, progress.StageLogin, "登录成功")
	return session, nil
}

func (s *Service) persistSession(accountID uint, session *connector.AuthSession) error {
	acc, err := s.Cipher.Encrypt(session.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refresh, err := s.Cipher.Encrypt(session.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	cookie, err := s.Cipher.Encrypt(session.Cookie)
	if err != nil {
		return fmt.Errorf("encrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Encrypt(session.CSRFToken)
	if err != nil {
		return fmt.Errorf("encrypt csrf: %w", err)
	}
	now := time.Now()
	expires := session.ExpiresAt
	return s.AuthSessions.Upsert(&storage.AuthSession{
		AccountID:          accountID,
		UserID:             session.UserID,
		AccessTokenCipher:  acc,
		RefreshTokenCipher: refresh,
		CookieCipher:       cookie,
		CSRFTokenCipher:    csrf,
		ExpiresAt:          &expires,
		LastLoginAt:        &now,
	})
}

func (s *Service) decryptSession(saved *storage.AuthSession) (*connector.AuthSession, error) {
	acc, err := s.Cipher.Decrypt(saved.AccessTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	refresh, err := s.Cipher.Decrypt(saved.RefreshTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	cookie, err := s.Cipher.Decrypt(saved.CookieCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Decrypt(saved.CSRFTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt csrf: %w", err)
	}
	expires := time.Time{}
	if saved.ExpiresAt != nil {
		expires = *saved.ExpiresAt
	}
	return &connector.AuthSession{
		UserID:       saved.UserID,
		AccessToken:  acc,
		RefreshToken: refresh,
		Cookie:       cookie,
		CSRFToken:    csrf,
		ExpiresAt:    expires,
	}, nil
}

// TestLogin 手动测试登录：
//   - password 模式：复用 login() 的完整流程（打码 → 登录 → 持久化）
//   - token 模式：直接走 EnsureSession，等同于检查 CheckAuth 是否通过
func (s *Service) TestLogin(ctx context.Context, accountID uint) error {
	c, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return err
	}
	s.applyHTTPConfig(conn)
	applyProxy(conn, resolved)
	if c.CredentialMode == storage.CredentialModeToken {
		_, err = s.EnsureSession(ctx, c, resolved, conn)
		return err
	}
	_, err = s.login(ctx, c, resolved, conn)
	return err
}

func (s *Service) RedeemCode(ctx context.Context, accountID uint, code string) (*connector.RedeemResult, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("兑换码不能为空")
	}

	c, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, err
	}
	s.applyHTTPConfig(conn)
	applyProxy(conn, resolved)
	session, err := s.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, err
	}

	result, err := conn.RedeemCode(ctx, resolved, session, code)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")

	if result != nil && result.NewBalance != nil {
		sampledAt := time.Now()
		_ = s.Accounts.UpdateBalance(c.ID, *result.NewBalance, &sampledAt, "")
		if s.Rates != nil {
			_ = s.Rates.AppendBalance(&storage.BalanceSnapshot{
				AccountID: c.ID,
				Balance:   *result.NewBalance,
				SampledAt: sampledAt,
			})
		}
		return result, nil
	}

	if result != nil && result.Type == "balance" && s.Rates != nil {
		bal, balErr := conn.GetBalance(ctx, resolved, session)
		if balErr == nil && bal != nil {
			sampledAt := bal.SampledAt
			if sampledAt.IsZero() {
				sampledAt = time.Now()
			}
			_ = s.Accounts.UpdateBalance(c.ID, bal.Balance, &sampledAt, "")
			if s.Rates != nil {
				_ = s.Rates.AppendBalance(&storage.BalanceSnapshot{
					AccountID: c.ID,
					Balance:   bal.Balance,
					SampledAt: sampledAt,
				})
			}
			result.NewBalance = &bal.Balance
		}
	}

	return result, nil
}

func (s *Service) GetRechargeInfo(ctx context.Context, accountID uint) (*connector.RechargeInfo, error) {
	c, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, err
	}
	s.applyHTTPConfig(conn)
	applyProxy(conn, resolved)
	session, err := s.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, err
	}
	info, err := conn.GetRechargeInfo(ctx, resolved, session)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return info, nil
}

func (s *Service) CreateRecharge(ctx context.Context, accountID uint, req connector.RechargeRequest) (*connector.RechargeLaunch, error) {
	c, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, err
	}
	s.applyHTTPConfig(conn)
	applyProxy(conn, resolved)
	session, err := s.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, err
	}
	launch, err := conn.CreateRecharge(ctx, resolved, session, req)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return launch, nil
}

func (s *Service) GetSubscriptionInfo(ctx context.Context, accountID uint) (*connector.SubscriptionInfo, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if resolved.Type != connector.TypeSub2API {
		return nil, errors.New("仅 Sub2API 支持订阅购买")
	}
	info, err := conn.GetSubscriptionInfo(ctx, resolved, session)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return info, nil
}

func (s *Service) CreateSubscription(ctx context.Context, accountID uint, req connector.SubscriptionRequest) (*connector.SubscriptionLaunch, error) {
	req.PlanID = strings.TrimSpace(req.PlanID)
	req.PaymentMethod = strings.TrimSpace(req.PaymentMethod)
	if req.PlanID == "" {
		return nil, errors.New("请选择订阅套餐")
	}
	if req.PaymentMethod == "" {
		return nil, errors.New("请选择支付方式")
	}
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if resolved.Type != connector.TypeSub2API {
		return nil, errors.New("仅 Sub2API 支持订阅购买")
	}
	launch, err := conn.CreateSubscription(ctx, resolved, session, req)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return launch, nil
}

func (s *Service) GetSubscriptionUsage(ctx context.Context, accountID uint) (*connector.SubscriptionUsageInfo, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if resolved.Type != connector.TypeSub2API {
		return nil, errors.New("仅 Sub2API 支持订阅用量")
	}
	info, err := conn.GetSubscriptionUsage(ctx, resolved, session)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return info, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, accountID uint, query connector.APIKeyQuery) (*connector.APIKeyPage, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	page, err := conn.ListAPIKeys(ctx, resolved, session, query)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return page, nil
}

func (s *Service) ListAPIKeyGroups(ctx context.Context, accountID uint) ([]connector.APIKeyGroup, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	groups, err := conn.ListAPIKeyGroups(ctx, resolved, session)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return groups, nil
}

func (s *Service) CreateAPIKey(ctx context.Context, accountID uint, req connector.APIKeyCreateRequest) (*connector.APIKey, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	key, err := conn.CreateAPIKey(ctx, resolved, session, req)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return key, nil
}

func (s *Service) UpdateAPIKey(ctx context.Context, accountID uint, keyID int64, req connector.APIKeyUpdateRequest) (*connector.APIKey, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return nil, err
	}
	key, err := conn.UpdateAPIKey(ctx, resolved, session, keyID, req)
	if err != nil {
		return nil, err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return key, nil
}

func (s *Service) DeleteAPIKey(ctx context.Context, accountID uint, keyID int64) error {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return err
	}
	if err := conn.DeleteAPIKey(ctx, resolved, session, keyID); err != nil {
		return err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return nil
}

func (s *Service) RevealAPIKey(ctx context.Context, accountID uint, keyID int64) (string, error) {
	c, resolved, conn, session, err := s.prepareConnectorCall(ctx, accountID)
	if err != nil {
		return "", err
	}
	key, err := conn.RevealAPIKey(ctx, resolved, session, keyID)
	if err != nil {
		return "", err
	}
	_ = s.Accounts.SetLastError(c.ID, "")
	return key, nil
}

func (s *Service) prepareConnectorCall(ctx context.Context, accountID uint) (*storage.UpstreamAccount, *connector.AccountTarget, connector.Connector, *connector.AuthSession, error) {
	c, err := s.Accounts.FindByID(accountID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	s.applyHTTPConfig(conn)
	applyProxy(conn, resolved)
	session, err := s.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return c, resolved, conn, session, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
