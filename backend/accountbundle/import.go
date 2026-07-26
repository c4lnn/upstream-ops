package accountbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type targetState struct {
	sites          []storage.UpstreamSite
	accounts       []storage.UpstreamAccount
	captchas       []storage.CaptchaConfig
	siteByName     map[string]*storage.UpstreamSite
	accountsBySite map[uint]map[string]*storage.UpstreamAccount
	captchaByName  map[string]*storage.CaptchaConfig
}

func (s *Service) Preview(ctx context.Context, data []byte, options ImportOptions) (*ImportPlan, error) {
	bundle, credentials, bundleDigest, err := parseBundle(data, options.Password)
	if err != nil {
		return nil, err
	}
	return s.buildPlan(s.db.WithContext(ctx), bundle, credentials, bundleDigest, options)
}

func (s *Service) Import(ctx context.Context, data []byte, options ImportOptions, expectedDigest string) (*ImportResult, error) {
	if strings.TrimSpace(expectedDigest) == "" {
		return nil, errors.New("缺少预检 digest")
	}
	bundle, credentials, bundleDigest, err := parseBundle(data, options.Password)
	if err != nil {
		return nil, err
	}
	var result *ImportResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		plan, err := s.buildPlan(tx, bundle, credentials, bundleDigest, options)
		if err != nil {
			return err
		}
		if plan.Digest != expectedDigest {
			return ErrPreviewStale
		}
		if plan.HasConflicts {
			return ErrImportConflict
		}
		if err := s.applyPlan(tx, bundle, credentials, plan); err != nil {
			if errors.Is(err, ErrPreviewStale) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrImportFailed, err)
		}
		result = &ImportResult{
			Digest:   plan.Digest,
			Summary:  plan.Summary,
			Items:    plan.Items,
			Warnings: plan.Warnings,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) buildPlan(db *gorm.DB, bundle *Bundle, credentials map[string]CredentialSecret, bundleDigest string, options ImportOptions) (*ImportPlan, error) {
	strategy := options.Strategy
	if strategy == "" {
		strategy = StrategyCreateOnly
	}
	if strategy != StrategyCreateOnly && strategy != StrategyUpsert {
		return nil, fmt.Errorf("不支持的导入策略: %s", strategy)
	}
	state, err := loadTargetState(db)
	if err != nil {
		return nil, err
	}
	stateDigest, err := digestTargetState(state)
	if err != nil {
		return nil, err
	}
	plan := &ImportPlan{
		BundleDigest: bundleDigest,
		Digest:       combinedPlanDigest(bundleDigest, strategy, stateDigest),
		Strategy:     strategy,
		Items:        make([]PlanItem, 0),
		bundle:       bundle,
		credentials:  credentials,
		targetDigest: stateDigest,
	}

	for _, siteConfig := range bundle.Sites {
		existingSite := state.siteByName[normalizeName(siteConfig.Name)]
		siteItem := PlanItem{Scope: "site", Key: siteConfig.Key, Name: siteConfig.Name}
		typeConflict := false
		baseURLChanged := false
		if existingSite == nil {
			siteItem.Action = ActionCreate
		} else if existingSite.Type != siteConfig.Type {
			typeConflict = true
			siteItem.Action = ActionConflict
			siteItem.Blocking = true
			siteItem.ExistingID = existingSite.ID
			siteItem.Message = fmt.Sprintf("同名站点类型冲突：目标为 %s，配置包为 %s", existingSite.Type, siteConfig.Type)
		} else {
			siteItem.ExistingID = existingSite.ID
			if strategy == StrategyCreateOnly {
				siteItem.Action = ActionSkip
				siteItem.Message = "站点已存在，仅新增策略不会更新站点配置"
			} else {
				siteItem.Changes = siteChanges(existingSite, siteConfig)
				baseURLChanged = existingSite.BaseURL != siteConfig.BaseURL
				if baseURLChanged {
					change := BaseURLChange{
						SiteKey:              siteConfig.Key,
						SiteName:             siteConfig.Name,
						Before:               existingSite.BaseURL,
						After:                siteConfig.BaseURL,
						AffectedAccountCount: len(state.accountsBySite[existingSite.ID]),
					}
					plan.BaseURLChanges = append(plan.BaseURLChanges, change)
					if !options.ConfirmBaseURLChanges {
						siteItem.Action = ActionConflict
						siteItem.Blocking = true
						siteItem.Message = fmt.Sprintf("站点入口变更将清除 %d 个账号会话并暂停其监控，需显式确认", change.AffectedAccountCount)
					}
				}
				if siteItem.Action != ActionConflict {
					if len(siteItem.Changes) == 0 {
						siteItem.Action = ActionSkip
					} else {
						siteItem.Action = ActionUpdate
					}
				}
			}
		}

		if !typeConflict && existingSite != nil && strategy == StrategyUpsert {
			currentDefault := currentDefaultAccountAlias(existingSite, state.accountsBySite[existingSite.ID])
			desiredDefault := accountAliasByKey(siteConfig, siteConfig.DefaultAccount)
			addChange(&siteItem.Changes, "default_account", currentDefault, desiredDefault)
			if siteItem.Action != ActionConflict && len(siteItem.Changes) > 0 {
				siteItem.Action = ActionUpdate
			}
		}
		plan.Items = append(plan.Items, siteItem)

		if typeConflict {
			continue
		}
		var existingAccounts map[string]*storage.UpstreamAccount
		if existingSite != nil {
			existingAccounts = state.accountsBySite[existingSite.ID]
		}
		for _, accountConfig := range siteConfig.Accounts {
			accountItem := PlanItem{Scope: "account", Key: accountConfig.Key, ParentKey: siteConfig.Key, Name: accountConfig.Alias}
			var existingAccount *storage.UpstreamAccount
			if existingAccounts != nil {
				existingAccount = existingAccounts[normalizeName(accountConfig.Alias)]
			}

			secret, hasCredential := credentials[accountConfig.Key]
			captchaID, captchaFound := resolveCaptchaID(state, accountConfig.CaptchaConfigName)
			if accountConfig.CaptchaConfigName != "" && !captchaFound {
				accountItem.Warnings = append(accountItem.Warnings, fmt.Sprintf("目标实例缺少验证码配置 %q，将取消绑定", accountConfig.CaptchaConfigName))
			}
			if existingAccount == nil {
				accountItem.Action = ActionCreate
				accountItem.NeedsCredential = !hasCredential
				if !hasCredential {
					accountItem.Warnings = append(accountItem.Warnings, "配置包未携带凭据，新账号将关闭监控")
				}
			} else {
				accountItem.ExistingID = existingAccount.ID
				accountItem.NeedsCredential = !hasCredential && existingAccount.PasswordCipher == ""
				if strategy == StrategyCreateOnly {
					accountItem.Action = ActionSkip
					accountItem.Message = "账号已存在"
				} else {
					desired := desiredAccount(existingAccount, siteConfig, accountConfig, captchaID, hasCredential)
					if baseURLChanged {
						desired.MonitorEnabled = false
					}
					if !hasCredential && normalizedCredentialMode(existingAccount.CredentialMode) != accountConfig.CredentialMode {
						accountItem.Warnings = append(accountItem.Warnings, "配置包未携带凭据，将保留目标账号的凭据模式")
					}
					accountItem.Changes = accountChanges(existingAccount, &desired)
					if hasCredential {
						changed, warning := s.credentialChanged(existingAccount, secret)
						if changed {
							accountItem.Changes = append(accountItem.Changes, FieldChange{Field: "credential", Before: "***", After: "***"})
						}
						if warning != "" {
							accountItem.Warnings = append(accountItem.Warnings, warning)
						}
					}
					if len(accountItem.Changes) == 0 {
						accountItem.Action = ActionSkip
					} else {
						accountItem.Action = ActionUpdate
					}
				}
			}
			plan.Items = append(plan.Items, accountItem)
		}
	}
	plan.RequiresBaseURLConfirmation = len(plan.BaseURLChanges) > 0 && !options.ConfirmBaseURLChanges
	finalizePlan(plan)
	return plan, nil
}

func loadTargetState(db *gorm.DB) (*targetState, error) {
	state := &targetState{
		siteByName:     make(map[string]*storage.UpstreamSite),
		accountsBySite: make(map[uint]map[string]*storage.UpstreamAccount),
		captchaByName:  make(map[string]*storage.CaptchaConfig),
	}
	if err := db.Order("id ASC").Find(&state.sites).Error; err != nil {
		return nil, fmt.Errorf("读取目标站点失败: %w", err)
	}
	if err := db.Order("id ASC").Find(&state.accounts).Error; err != nil {
		return nil, fmt.Errorf("读取目标账号失败: %w", err)
	}
	if err := db.Order("id ASC").Find(&state.captchas).Error; err != nil {
		return nil, fmt.Errorf("读取目标验证码配置失败: %w", err)
	}
	for index := range state.sites {
		site := &state.sites[index]
		key := normalizeName(site.Name)
		if _, exists := state.siteByName[key]; exists {
			return nil, fmt.Errorf("目标实例存在规范化后重名的站点: %s", site.Name)
		}
		state.siteByName[key] = site
	}
	for index := range state.accounts {
		account := &state.accounts[index]
		accounts := state.accountsBySite[account.SiteID]
		if accounts == nil {
			accounts = make(map[string]*storage.UpstreamAccount)
			state.accountsBySite[account.SiteID] = accounts
		}
		key := normalizeName(account.Alias)
		if _, exists := accounts[key]; exists {
			return nil, fmt.Errorf("站点 %d 存在规范化后重名的账号别名: %s", account.SiteID, account.Alias)
		}
		accounts[key] = account
	}
	for index := range state.captchas {
		captcha := &state.captchas[index]
		state.captchaByName[normalizeName(captcha.Name)] = captcha
	}
	return state, nil
}

func resolveCaptchaID(state *targetState, name string) (*uint, bool) {
	if strings.TrimSpace(name) == "" {
		return nil, true
	}
	captcha := state.captchaByName[normalizeName(name)]
	if captcha == nil {
		return nil, false
	}
	id := captcha.ID
	return &id, true
}

func desiredAccount(existing *storage.UpstreamAccount, site SiteConfig, account AccountConfig, captchaID *uint, hasCredential bool) storage.UpstreamAccount {
	desired := storage.UpstreamAccount{}
	if existing != nil {
		desired = *existing
	}
	desired.Alias = account.Alias
	desired.Username = account.Username
	desired.AccountSortOrder = account.AccountSortOrder
	desired.LoginExtraParams = strings.TrimSpace(account.LoginExtraParams)
	desired.SubscriptionEnabled = site.Type == storage.UpstreamTypeSub2API && account.SubscriptionEnabled
	desired.ProxyEnabled = account.ProxyEnabled
	desired.BalanceThreshold = account.BalanceThreshold
	desired.RechargeMultiplier = normalizedMultiplier(account.RechargeMultiplier)
	desired.RechargeMultiplierMode = connector.NormalizeRechargeMultiplierMode(account.RechargeMultiplierMode)
	desired.MonitorEnabled = account.MonitorEnabled

	mode := account.CredentialMode
	if existing != nil && !hasCredential {
		mode = normalizedCredentialMode(existing.CredentialMode)
	}
	desired.CredentialMode = mode
	if !hasCredential && (existing == nil || existing.PasswordCipher == "") {
		desired.MonitorEnabled = false
	}
	if mode == storage.CredentialModeToken {
		desired.TurnstileEnabled = false
		desired.CaptchaConfigID = nil
	} else {
		desired.TurnstileEnabled = account.TurnstileEnabled
		desired.CaptchaConfigID = captchaID
	}
	return desired
}

func normalizedMultiplier(value *float64) *float64 {
	if value == nil || *value <= 0 {
		return nil
	}
	result := *value
	return &result
}

func (s *Service) credentialChanged(existing *storage.UpstreamAccount, desired CredentialSecret) (bool, string) {
	if normalizedCredentialMode(existing.CredentialMode) != desired.Mode || existing.PasswordCipher == "" {
		return true, ""
	}
	raw, err := s.cipher.Decrypt(existing.PasswordCipher)
	if err != nil {
		return true, "目标账号现有凭据无法解密，将使用配置包凭据替换"
	}
	return raw != desired.Value, ""
}

func siteChanges(existing *storage.UpstreamSite, desired SiteConfig) []FieldChange {
	changes := make([]FieldChange, 0)
	addChange(&changes, "name", existing.Name, desired.Name)
	addChange(&changes, "base_url", existing.BaseURL, desired.BaseURL)
	addChange(&changes, "sort_order", existing.SortOrder, desired.SortOrder)
	addChange(&changes, "ignore_announcements", existing.IgnoreAnnouncements, desired.IgnoreAnnouncements)
	return changes
}

func accountChanges(existing, desired *storage.UpstreamAccount) []FieldChange {
	changes := make([]FieldChange, 0)
	addChange(&changes, "alias", existing.Alias, desired.Alias)
	addChange(&changes, "username", existing.Username, desired.Username)
	addChange(&changes, "account_sort_order", existing.AccountSortOrder, desired.AccountSortOrder)
	addChange(&changes, "credential_mode", normalizedCredentialMode(existing.CredentialMode), desired.CredentialMode)
	addChange(&changes, "login_extra_params", existing.LoginExtraParams, desired.LoginExtraParams)
	addChange(&changes, "turnstile_enabled", existing.TurnstileEnabled, desired.TurnstileEnabled)
	addChange(&changes, "subscription_enabled", existing.SubscriptionEnabled, desired.SubscriptionEnabled)
	addChange(&changes, "proxy_enabled", existing.ProxyEnabled, desired.ProxyEnabled)
	addChange(&changes, "captcha_config_id", existing.CaptchaConfigID, desired.CaptchaConfigID)
	addChange(&changes, "balance_threshold", existing.BalanceThreshold, desired.BalanceThreshold)
	addChange(&changes, "recharge_multiplier", existing.RechargeMultiplier, desired.RechargeMultiplier)
	addChange(&changes, "recharge_multiplier_mode", connector.NormalizeRechargeMultiplierMode(existing.RechargeMultiplierMode), desired.RechargeMultiplierMode)
	addChange(&changes, "monitor_enabled", existing.MonitorEnabled, desired.MonitorEnabled)
	return changes
}

func addChange(changes *[]FieldChange, field string, before, after any) {
	if !reflect.DeepEqual(before, after) {
		*changes = append(*changes, FieldChange{Field: field, Before: before, After: after})
	}
}

func accountAliasByKey(site SiteConfig, key string) string {
	for _, account := range site.Accounts {
		if account.Key == key {
			return account.Alias
		}
	}
	return ""
}

func currentDefaultAccountAlias(site *storage.UpstreamSite, accounts map[string]*storage.UpstreamAccount) string {
	for _, account := range accounts {
		if account.ID == site.DefaultAccountID {
			return account.Alias
		}
	}
	return ""
}

func finalizePlan(plan *ImportPlan) {
	for _, item := range plan.Items {
		switch item.Action {
		case ActionCreate:
			plan.Summary.Create++
		case ActionUpdate:
			plan.Summary.Update++
		case ActionSkip:
			plan.Summary.Skip++
		case ActionConflict:
			plan.Summary.Conflict++
		}
		plan.Summary.Warnings += len(item.Warnings)
		if item.NeedsCredential {
			plan.Summary.NeedCredential++
		}
		if item.Blocking {
			plan.HasConflicts = true
		}
	}
}

type targetDigestSite struct {
	ID                  uint                 `json:"id"`
	Name                string               `json:"name"`
	Type                storage.UpstreamType `json:"type"`
	BaseURL             string               `json:"base_url"`
	SortOrder           int                  `json:"sort_order"`
	DefaultAccountID    uint                 `json:"default_account_id"`
	IgnoreAnnouncements bool                 `json:"ignore_announcements"`
}

type targetDigestAccount struct {
	ID                     uint                   `json:"id"`
	SiteID                 uint                   `json:"site_id"`
	Alias                  string                 `json:"alias"`
	Username               string                 `json:"username"`
	AccountSortOrder       int                    `json:"account_sort_order"`
	PasswordCipher         string                 `json:"password_cipher"`
	CredentialMode         storage.CredentialMode `json:"credential_mode"`
	LoginExtraParams       string                 `json:"login_extra_params"`
	TurnstileEnabled       bool                   `json:"turnstile_enabled"`
	SubscriptionEnabled    bool                   `json:"subscription_enabled"`
	ProxyEnabled           bool                   `json:"proxy_enabled"`
	CaptchaConfigID        *uint                  `json:"captcha_config_id"`
	BalanceThreshold       float64                `json:"balance_threshold"`
	RechargeMultiplier     *float64               `json:"recharge_multiplier"`
	RechargeMultiplierMode string                 `json:"recharge_multiplier_mode"`
	MonitorEnabled         bool                   `json:"monitor_enabled"`
}

func digestTargetState(state *targetState) (string, error) {
	sites := make([]targetDigestSite, 0, len(state.sites))
	for _, site := range state.sites {
		sites = append(sites, targetDigestSite{
			ID: site.ID, Name: site.Name, Type: site.Type, BaseURL: site.BaseURL, SortOrder: site.SortOrder,
			DefaultAccountID: site.DefaultAccountID, IgnoreAnnouncements: site.IgnoreAnnouncements,
		})
	}
	accounts := make([]targetDigestAccount, 0, len(state.accounts))
	for _, account := range state.accounts {
		accounts = append(accounts, targetDigestAccount{
			ID: account.ID, SiteID: account.SiteID, Alias: account.Alias, Username: account.Username,
			AccountSortOrder: account.AccountSortOrder, PasswordCipher: account.PasswordCipher,
			CredentialMode: account.CredentialMode, LoginExtraParams: account.LoginExtraParams,
			TurnstileEnabled: account.TurnstileEnabled, SubscriptionEnabled: account.SubscriptionEnabled,
			ProxyEnabled: account.ProxyEnabled, CaptchaConfigID: account.CaptchaConfigID,
			BalanceThreshold: account.BalanceThreshold, RechargeMultiplier: account.RechargeMultiplier,
			RechargeMultiplierMode: account.RechargeMultiplierMode, MonitorEnabled: account.MonitorEnabled,
		})
	}
	captchaNames := make([]string, 0, len(state.captchas))
	for _, captcha := range state.captchas {
		captchaNames = append(captchaNames, fmt.Sprintf("%d:%s", captcha.ID, normalizeName(captcha.Name)))
	}
	sort.Strings(captchaNames)
	encoded, err := json.Marshal(struct {
		Sites    []targetDigestSite    `json:"sites"`
		Accounts []targetDigestAccount `json:"accounts"`
		Captchas []string              `json:"captchas"`
	}{Sites: sites, Accounts: accounts, Captchas: captchaNames})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func combinedPlanDigest(bundleDigest string, strategy ImportStrategy, stateDigest string) string {
	sum := sha256.Sum256([]byte(bundleDigest + "\n" + string(strategy) + "\n" + stateDigest))
	return hex.EncodeToString(sum[:])
}

func (s *Service) applyPlan(tx *gorm.DB, bundle *Bundle, credentials map[string]CredentialSecret, plan *ImportPlan) error {
	state, err := loadTargetState(tx)
	if err != nil {
		return err
	}
	stateDigest, err := digestTargetState(state)
	if err != nil {
		return err
	}
	if stateDigest != plan.targetDigest {
		return ErrPreviewStale
	}
	strategy := plan.Strategy
	sitesRepo := storage.NewUpstreamSites(tx)
	accountsRepo := storage.NewUpstreamAccounts(tx)

	for _, siteConfig := range bundle.Sites {
		site := state.siteByName[normalizeName(siteConfig.Name)]
		createdSite := false
		baseURLChanged := false
		if site == nil {
			site = &storage.UpstreamSite{
				Name:                siteConfig.Name,
				Type:                siteConfig.Type,
				BaseURL:             siteConfig.BaseURL,
				SortOrder:           siteConfig.SortOrder,
				IgnoreAnnouncements: siteConfig.IgnoreAnnouncements,
			}
			if err := sitesRepo.Create(site); err != nil {
				return fmt.Errorf("创建站点 %s 失败: %w", siteConfig.Name, err)
			}
			createdSite = true
			state.siteByName[normalizeName(site.Name)] = site
			state.accountsBySite[site.ID] = make(map[string]*storage.UpstreamAccount)
		} else if strategy == StrategyUpsert {
			baseURLChanged = site.BaseURL != siteConfig.BaseURL
			if baseURLChanged {
				if err := tx.Model(&storage.UpstreamSite{}).Where("id = ?", site.ID).Updates(map[string]any{
					"name":                 siteConfig.Name,
					"sort_order":           siteConfig.SortOrder,
					"ignore_announcements": siteConfig.IgnoreAnnouncements,
				}).Error; err != nil {
					return fmt.Errorf("更新站点 %s 的基础配置失败: %w", siteConfig.Name, err)
				}
				if _, err := sitesRepo.UpdateBaseURL(site.ID, siteConfig.BaseURL, true); err != nil {
					return fmt.Errorf("更新站点 %s 的入口失败: %w", siteConfig.Name, err)
				}
				site.Name = siteConfig.Name
				site.BaseURL = siteConfig.BaseURL
				site.SortOrder = siteConfig.SortOrder
				site.IgnoreAnnouncements = siteConfig.IgnoreAnnouncements
			} else {
				site.Name = siteConfig.Name
				site.BaseURL = siteConfig.BaseURL
				site.SortOrder = siteConfig.SortOrder
				site.IgnoreAnnouncements = siteConfig.IgnoreAnnouncements
				if err := sitesRepo.Update(site); err != nil {
					return fmt.Errorf("更新站点 %s 失败: %w", siteConfig.Name, err)
				}
			}
		}

		accountTargets := state.accountsBySite[site.ID]
		if accountTargets == nil {
			accountTargets = make(map[string]*storage.UpstreamAccount)
			state.accountsBySite[site.ID] = accountTargets
		}
		accountByKey := make(map[string]*storage.UpstreamAccount, len(siteConfig.Accounts))
		for _, accountConfig := range siteConfig.Accounts {
			account := accountTargets[normalizeName(accountConfig.Alias)]
			if account != nil && strategy == StrategyCreateOnly {
				accountByKey[accountConfig.Key] = account
				continue
			}
			secret, hasCredential := credentials[accountConfig.Key]
			captchaID, _ := resolveCaptchaID(state, accountConfig.CaptchaConfigName)
			desired := desiredAccount(account, siteConfig, accountConfig, captchaID, hasCredential)
			desired.SiteID = site.ID
			if account != nil && baseURLChanged {
				desired.MonitorEnabled = false
			}

			credentialChanged := false
			if hasCredential {
				if account == nil {
					credentialChanged = true
				} else {
					credentialChanged, _ = s.credentialChanged(account, secret)
				}
				if credentialChanged {
					encrypted, err := s.cipher.Encrypt(secret.Value)
					if err != nil {
						return fmt.Errorf("加密账号 %s 的凭据失败: %w", accountConfig.Alias, err)
					}
					desired.PasswordCipher = encrypted
				}
			}

			if account == nil {
				monitorEnabled := desired.MonitorEnabled
				if err := accountsRepo.Create(&desired); err != nil {
					return fmt.Errorf("创建账号 %s 失败: %w", accountConfig.Alias, err)
				}
				if !monitorEnabled {
					if err := tx.Model(&storage.UpstreamAccount{}).Where("id = ?", desired.ID).Update("monitor_enabled", false).Error; err != nil {
						return fmt.Errorf("关闭账号 %s 的监控失败: %w", accountConfig.Alias, err)
					}
					desired.MonitorEnabled = false
				}
				account = &desired
				accountTargets[normalizeName(account.Alias)] = account
			} else {
				identityChanged := credentialChanged ||
					account.Username != desired.Username ||
					account.CredentialMode != desired.CredentialMode ||
					account.LoginExtraParams != desired.LoginExtraParams
				desired.ID = account.ID
				desired.CreatedAt = account.CreatedAt
				if err := accountsRepo.Update(&desired); err != nil {
					return fmt.Errorf("更新账号 %s 失败: %w", accountConfig.Alias, err)
				}
				if identityChanged && !baseURLChanged {
					if err := tx.Where("account_id = ?", account.ID).Delete(&storage.AuthSession{}).Error; err != nil {
						return fmt.Errorf("清理账号 %s 的旧会话失败: %w", accountConfig.Alias, err)
					}
				}
				*account = desired
			}
			accountByKey[accountConfig.Key] = account
		}

		if createdSite || strategy == StrategyUpsert {
			if siteConfig.DefaultAccount != "" {
				defaultAccount := accountByKey[siteConfig.DefaultAccount]
				if defaultAccount == nil {
					return fmt.Errorf("站点 %s 的默认账号无法解析", siteConfig.Name)
				}
				if err := tx.Model(&storage.UpstreamSite{}).Where("id = ?", site.ID).
					Update("default_account_id", defaultAccount.ID).Error; err != nil {
					return fmt.Errorf("设置站点 %s 的默认账号失败: %w", siteConfig.Name, err)
				}
				site.DefaultAccountID = defaultAccount.ID
			}
		}
	}
	return nil
}
