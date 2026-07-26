package accountbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
)

const (
	BundleSchema       = "upstream-ops/site-account-bundle"
	legacyBundleSchema = "upstream-ops/channel-bundle"
	BundleVersion      = 1
	MaxBundleSize      = 10 << 20
	MaxBundleSites     = 1000
	MaxBundleAccounts  = 10000
)

var (
	ErrBundleTooLarge = errors.New("站点账号配置包超过 10 MiB 限制")
	ErrImportConflict = errors.New("站点账号配置包存在阻断性冲突")
	ErrPreviewStale   = errors.New("导入内容或目标配置已变化，请重新预检")
	ErrImportFailed   = errors.New("站点账号配置包事务导入失败")
)

// Bundle 只保存站点和账号的声明式配置，不包含任何运行数据。
type Bundle struct {
	Schema      string              `json:"schema"`
	Version     int                 `json:"version"`
	ExportedAt  time.Time           `json:"exported_at"`
	Sites       []SiteConfig        `json:"sites"`
	Credentials *CredentialEnvelope `json:"credentials,omitempty"`
}

// SiteConfig 是一个上游实例；平台类型和入口地址只能出现在这里。
type SiteConfig struct {
	Key                 string               `json:"key"`
	Name                string               `json:"name"`
	Type                storage.UpstreamType `json:"type"`
	BaseURL             string               `json:"base_url"`
	SortOrder           int                  `json:"sort_order"`
	IgnoreAnnouncements bool                 `json:"ignore_announcements"`
	DefaultAccount      string               `json:"default_account,omitempty"`
	Accounts            []AccountConfig      `json:"accounts"`
}

// AccountConfig 只包含账号级配置。账号永不覆盖所属站点的类型或入口地址。
type AccountConfig struct {
	Key                    string                 `json:"key"`
	Alias                  string                 `json:"alias"`
	Username               string                 `json:"username,omitempty"`
	AccountSortOrder       int                    `json:"account_sort_order"`
	CredentialMode         storage.CredentialMode `json:"credential_mode"`
	CredentialIncluded     bool                   `json:"credential_included"`
	LoginExtraParams       string                 `json:"login_extra_params,omitempty"`
	TurnstileEnabled       bool                   `json:"turnstile_enabled"`
	SubscriptionEnabled    bool                   `json:"subscription_enabled"`
	ProxyEnabled           bool                   `json:"proxy_enabled"`
	CaptchaConfigName      string                 `json:"captcha_config_name,omitempty"`
	BalanceThreshold       float64                `json:"balance_threshold"`
	RechargeMultiplier     *float64               `json:"recharge_multiplier,omitempty"`
	RechargeMultiplierMode string                 `json:"recharge_multiplier_mode"`
	MonitorEnabled         bool                   `json:"monitor_enabled"`
}

type CredentialEnvelope struct {
	Version     int    `json:"version"`
	Algorithm   string `json:"algorithm"`
	KDF         string `json:"kdf"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	Salt        string `json:"salt"`
	Nonce       string `json:"nonce"`
	Ciphertext  string `json:"ciphertext"`
}

type CredentialSecret struct {
	Mode  storage.CredentialMode `json:"mode"`
	Value string                 `json:"value"`
}

type ExportOptions struct {
	SiteIDs            []uint
	IncludeCredentials bool
	Password           string
}

type ImportStrategy string

const (
	StrategyCreateOnly ImportStrategy = "create_only"
	StrategyUpsert     ImportStrategy = "upsert"
)

type ImportOptions struct {
	Strategy              ImportStrategy
	Password              string
	ConfirmBaseURLChanges bool
}

type PlanAction string

const (
	ActionCreate   PlanAction = "create"
	ActionUpdate   PlanAction = "update"
	ActionSkip     PlanAction = "skip"
	ActionConflict PlanAction = "conflict"
)

type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

type BaseURLChange struct {
	SiteKey              string `json:"site_key"`
	SiteName             string `json:"site_name"`
	Before               string `json:"before"`
	After                string `json:"after"`
	AffectedAccountCount int    `json:"affected_account_count"`
}

type PlanItem struct {
	Scope           string        `json:"scope"`
	Key             string        `json:"key"`
	ParentKey       string        `json:"parent_key,omitempty"`
	Name            string        `json:"name"`
	Action          PlanAction    `json:"action"`
	Blocking        bool          `json:"blocking"`
	NeedsCredential bool          `json:"needs_credential"`
	Changes         []FieldChange `json:"changes,omitempty"`
	Warnings        []string      `json:"warnings,omitempty"`
	Message         string        `json:"message,omitempty"`
	ExistingID      uint          `json:"-"`
}

type PlanSummary struct {
	Create         int `json:"create"`
	Update         int `json:"update"`
	Skip           int `json:"skip"`
	Conflict       int `json:"conflict"`
	Warnings       int `json:"warnings"`
	NeedCredential int `json:"need_credential"`
}

type ImportPlan struct {
	BundleDigest                string          `json:"bundle_digest"`
	Digest                      string          `json:"digest"`
	Strategy                    ImportStrategy  `json:"strategy"`
	Summary                     PlanSummary     `json:"summary"`
	HasConflicts                bool            `json:"has_conflicts"`
	RequiresBaseURLConfirmation bool            `json:"requires_base_url_confirmation"`
	BaseURLChanges              []BaseURLChange `json:"base_url_changes,omitempty"`
	Items                       []PlanItem      `json:"items"`
	Warnings                    []string        `json:"warnings,omitempty"`

	bundle       *Bundle
	credentials  map[string]CredentialSecret
	targetDigest string
}

type ImportResult struct {
	Digest   string      `json:"digest"`
	Summary  PlanSummary `json:"summary"`
	Items    []PlanItem  `json:"items"`
	Warnings []string    `json:"warnings,omitempty"`
}

func Decode(data []byte) (*Bundle, error) {
	if len(data) > MaxBundleSize {
		return nil, ErrBundleTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return nil, fmt.Errorf("解析站点账号配置包失败: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := normalizeAndValidate(&bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("站点账号配置包只能包含一个 JSON 对象")
		}
		return fmt.Errorf("解析站点账号配置包尾部失败: %w", err)
	}
	return nil
}

func normalizeAndValidate(bundle *Bundle) error {
	if bundle.Schema != BundleSchema {
		if bundle.Schema == legacyBundleSchema {
			return errors.New("不支持旧渠道配置包 schema: upstream-ops/channel-bundle")
		}
		return fmt.Errorf("不支持的站点账号配置包 schema: %q", bundle.Schema)
	}
	if bundle.Version != BundleVersion {
		return fmt.Errorf("不支持的站点账号配置包版本: %d", bundle.Version)
	}
	if bundle.ExportedAt.IsZero() {
		return errors.New("站点账号配置包缺少 exported_at")
	}
	if len(bundle.Sites) > MaxBundleSites {
		return fmt.Errorf("站点账号配置包站点数超过限制: %d", MaxBundleSites)
	}

	siteKeys := make(map[string]struct{}, len(bundle.Sites))
	siteNames := make(map[string]struct{}, len(bundle.Sites))
	accountKeys := make(map[string]struct{})
	accountCount := 0
	for siteIndex := range bundle.Sites {
		site := &bundle.Sites[siteIndex]
		site.Key = strings.TrimSpace(site.Key)
		site.Name = strings.TrimSpace(site.Name)
		site.BaseURL = strings.TrimSpace(site.BaseURL)
		if err := validateKey("站点", site.Key); err != nil {
			return err
		}
		if _, exists := siteKeys[site.Key]; exists {
			return fmt.Errorf("站点 key 重复: %s", site.Key)
		}
		siteKeys[site.Key] = struct{}{}
		if site.Name == "" || len(site.Name) > 128 {
			return fmt.Errorf("站点 %s 的名称为空或超过 128 字节", site.Key)
		}
		siteNameKey := normalizeName(site.Name)
		if _, exists := siteNames[siteNameKey]; exists {
			return fmt.Errorf("配置包存在规范化后重名的站点: %s", site.Name)
		}
		siteNames[siteNameKey] = struct{}{}
		if !validSiteType(site.Type) {
			return fmt.Errorf("站点 %s 的类型无效: %s", site.Key, site.Type)
		}
		baseURL, err := normalizeBaseURL(site.BaseURL)
		if err != nil {
			return fmt.Errorf("站点 %s 的 base_url 无效: %w", site.Key, err)
		}
		site.BaseURL = baseURL
		if site.SortOrder == 0 {
			site.SortOrder = 1
		}

		localAccounts := make(map[string]struct{}, len(site.Accounts))
		localAliases := make(map[string]struct{}, len(site.Accounts))
		for accountIndex := range site.Accounts {
			accountCount++
			if accountCount > MaxBundleAccounts {
				return fmt.Errorf("站点账号配置包账号数超过限制: %d", MaxBundleAccounts)
			}
			account := &site.Accounts[accountIndex]
			account.Key = strings.TrimSpace(account.Key)
			account.Alias = strings.TrimSpace(account.Alias)
			account.Username = strings.TrimSpace(account.Username)
			account.CaptchaConfigName = strings.TrimSpace(account.CaptchaConfigName)
			if err := validateKey("账号", account.Key); err != nil {
				return err
			}
			if _, exists := accountKeys[account.Key]; exists {
				return fmt.Errorf("账号 key 重复: %s", account.Key)
			}
			accountKeys[account.Key] = struct{}{}
			localAccounts[account.Key] = struct{}{}
			if account.Alias == "" || len(account.Alias) > 128 {
				return fmt.Errorf("账号 %s 的别名为空或超过 128 字节", account.Key)
			}
			aliasKey := normalizeName(account.Alias)
			if _, exists := localAliases[aliasKey]; exists {
				return fmt.Errorf("站点 %s 存在规范化后重名的账号别名: %s", site.Key, account.Alias)
			}
			localAliases[aliasKey] = struct{}{}
			if account.CredentialMode == "" {
				account.CredentialMode = storage.CredentialModePassword
			}
			if account.CredentialMode != storage.CredentialModePassword && account.CredentialMode != storage.CredentialModeToken {
				return fmt.Errorf("账号 %s 的凭据模式无效", account.Key)
			}
			if account.AccountSortOrder == 0 {
				account.AccountSortOrder = 1
			}
			if err := validateLoginExtra(account.Key, account.LoginExtraParams); err != nil {
				return err
			}
			if account.BalanceThreshold < 0 {
				return fmt.Errorf("账号 %s 的余额阈值不能为负数", account.Key)
			}
			if account.RechargeMultiplier != nil && *account.RechargeMultiplier <= 0 {
				account.RechargeMultiplier = nil
			}
			account.RechargeMultiplierMode = connector.NormalizeRechargeMultiplierMode(account.RechargeMultiplierMode)
			if account.CredentialMode == storage.CredentialModeToken {
				account.TurnstileEnabled = false
				account.CaptchaConfigName = ""
			}
			if site.Type != storage.UpstreamTypeSub2API {
				account.SubscriptionEnabled = false
			}
		}
		site.DefaultAccount = strings.TrimSpace(site.DefaultAccount)
		if len(site.Accounts) > 0 && site.DefaultAccount == "" {
			return fmt.Errorf("站点 %s 缺少默认账号引用", site.Key)
		}
		if site.DefaultAccount != "" {
			if _, exists := localAccounts[site.DefaultAccount]; !exists {
				return fmt.Errorf("站点 %s 的默认账号引用无效: %s", site.Key, site.DefaultAccount)
			}
		}
	}
	return nil
}

func validateKey(kind, key string) error {
	if key == "" || len(key) > 128 {
		return fmt.Errorf("%s key 为空或超过 128 字节", kind)
	}
	return nil
}

func validSiteType(value storage.UpstreamType) bool {
	return value == storage.UpstreamTypeNewAPI || value == storage.UpstreamTypeSub2API
}

func normalizeBaseURL(value string) (string, error) {
	if value == "" || len(value) > 512 {
		return "", errors.New("不能为空或超过 512 字节")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.ForceQuery || strings.Contains(value, "#") {
		return "", errors.New("不能包含查询参数或 fragment")
	}
	return storage.NormalizeBaseURL(value)
}

func validateLoginExtra(accountKey, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return fmt.Errorf("账号 %s 的登录附加参数必须是 JSON 对象", accountKey)
	}
	return nil
}

func CanonicalDigest(bundle *Bundle) (string, error) {
	encoded, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
