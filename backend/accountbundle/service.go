package accountbundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type Service struct {
	db     *gorm.DB
	cipher *crypto.Cipher
}

func NewService(db *gorm.DB, cipher *crypto.Cipher) *Service {
	return &Service{db: db, cipher: cipher}
}

func (s *Service) Export(ctx context.Context, options ExportOptions) ([]byte, error) {
	if len(options.SiteIDs) == 0 {
		return nil, errors.New("至少选择一个站点")
	}
	if options.IncludeCredentials && strings.TrimSpace(options.Password) == "" {
		return nil, errors.New("包含凭据时必须提供非空导出密码")
	}

	siteIDs := append([]uint(nil), options.SiteIDs...)
	sort.Slice(siteIDs, func(i, j int) bool { return siteIDs[i] < siteIDs[j] })
	uniqueIDs := siteIDs[:0]
	for _, id := range siteIDs {
		if id == 0 {
			return nil, errors.New("站点 ID 必须为正整数")
		}
		if len(uniqueIDs) == 0 || uniqueIDs[len(uniqueIDs)-1] != id {
			uniqueIDs = append(uniqueIDs, id)
		}
	}

	db := s.db.WithContext(ctx)
	var captchaList []storage.CaptchaConfig
	if err := db.Order("id ASC").Find(&captchaList).Error; err != nil {
		return nil, fmt.Errorf("读取验证码配置失败: %w", err)
	}
	captchaNames := make(map[uint]string, len(captchaList))
	for _, captcha := range captchaList {
		captchaNames[captcha.ID] = captcha.Name
	}

	bundle := &Bundle{
		Schema:     BundleSchema,
		Version:    BundleVersion,
		ExportedAt: time.Now().UTC(),
		Sites:      make([]SiteConfig, 0, len(uniqueIDs)),
	}
	credentials := make(map[string]CredentialSecret)
	accountSequence := 0
	for siteIndex, siteID := range uniqueIDs {
		var site storage.UpstreamSite
		if err := db.First(&site, siteID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("站点不存在: %d", siteID)
			}
			return nil, fmt.Errorf("读取站点 %d 失败: %w", siteID, err)
		}
		var accounts []storage.UpstreamAccount
		if err := db.Where("site_id = ?", site.ID).
			Order("account_sort_order DESC").Order("id ASC").Find(&accounts).Error; err != nil {
			return nil, fmt.Errorf("读取站点 %s 的账号失败: %w", site.Name, err)
		}
		siteConfig := SiteConfig{
			Key:                 fmt.Sprintf("site-%d", siteIndex+1),
			Name:                site.Name,
			Type:                site.Type,
			BaseURL:             site.BaseURL,
			SortOrder:           site.SortOrder,
			IgnoreAnnouncements: site.IgnoreAnnouncements,
			Accounts:            make([]AccountConfig, 0, len(accounts)),
		}
		for _, account := range accounts {
			accountSequence++
			accountKey := fmt.Sprintf("account-%d", accountSequence)
			accountConfig := AccountConfig{
				Key:                    accountKey,
				Alias:                  account.Alias,
				Username:               account.Username,
				AccountSortOrder:       account.AccountSortOrder,
				CredentialMode:         normalizedCredentialMode(account.CredentialMode),
				LoginExtraParams:       account.LoginExtraParams,
				TurnstileEnabled:       account.TurnstileEnabled,
				SubscriptionEnabled:    account.SubscriptionEnabled,
				ProxyEnabled:           account.ProxyEnabled,
				BalanceThreshold:       account.BalanceThreshold,
				RechargeMultiplier:     normalizedMultiplier(account.RechargeMultiplier),
				RechargeMultiplierMode: account.RechargeMultiplierMode,
				MonitorEnabled:         account.MonitorEnabled,
			}
			if account.CaptchaConfigID != nil {
				accountConfig.CaptchaConfigName = captchaNames[*account.CaptchaConfigID]
			}
			if options.IncludeCredentials && account.PasswordCipher != "" {
				raw, err := s.cipher.Decrypt(account.PasswordCipher)
				if err != nil {
					return nil, fmt.Errorf("解密账号 %s 的凭据失败: %w", account.Alias, err)
				}
				if strings.TrimSpace(raw) != "" {
					accountConfig.CredentialIncluded = true
					credentials[accountKey] = CredentialSecret{Mode: accountConfig.CredentialMode, Value: raw}
				}
			}
			if account.ID == site.DefaultAccountID {
				siteConfig.DefaultAccount = accountKey
			}
			siteConfig.Accounts = append(siteConfig.Accounts, accountConfig)
		}
		if len(siteConfig.Accounts) > 0 && siteConfig.DefaultAccount == "" {
			return nil, fmt.Errorf("站点 %s 没有有效默认账号", site.Name)
		}
		bundle.Sites = append(bundle.Sites, siteConfig)
	}
	if len(credentials) > 0 {
		aad, err := credentialAAD(bundle)
		if err != nil {
			return nil, err
		}
		envelope, err := sealCredentials(options.Password, credentials, aad)
		if err != nil {
			return nil, err
		}
		bundle.Credentials = envelope
	}
	encoded, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码站点账号配置包失败: %w", err)
	}
	return append(encoded, '\n'), nil
}

func normalizedCredentialMode(mode storage.CredentialMode) storage.CredentialMode {
	if mode == "" {
		return storage.CredentialModePassword
	}
	return mode
}

func parseBundle(data []byte, password string) (*Bundle, map[string]CredentialSecret, string, error) {
	bundle, err := Decode(data)
	if err != nil {
		return nil, nil, "", err
	}
	digest, err := CanonicalDigest(bundle)
	if err != nil {
		return nil, nil, "", fmt.Errorf("计算配置包 digest 失败: %w", err)
	}
	aad, err := credentialAAD(bundle)
	if err != nil {
		return nil, nil, "", err
	}
	credentials, err := openCredentials(password, bundle.Credentials, aad)
	if err != nil {
		return nil, nil, "", err
	}
	accountByKey := make(map[string]AccountConfig)
	for _, site := range bundle.Sites {
		for _, account := range site.Accounts {
			accountByKey[account.Key] = account
			secret, exists := credentials[account.Key]
			if account.CredentialIncluded != exists {
				return nil, nil, "", fmt.Errorf("账号 %s 的凭据标记与加密信封不一致", account.Key)
			}
			if exists {
				if secret.Mode != account.CredentialMode {
					return nil, nil, "", fmt.Errorf("账号 %s 的凭据模式与加密信封不一致", account.Key)
				}
				if err := validateCredential(site.Type, secret); err != nil {
					return nil, nil, "", fmt.Errorf("账号 %s 的凭据无效: %w", account.Key, err)
				}
			}
		}
	}
	for key := range credentials {
		if _, exists := accountByKey[key]; !exists {
			return nil, nil, "", fmt.Errorf("凭据引用了不存在的账号: %s", key)
		}
	}
	return bundle, credentials, digest, nil
}

func validateCredential(siteType storage.UpstreamType, secret CredentialSecret) error {
	if strings.TrimSpace(secret.Value) == "" {
		return errors.New("凭据不能为空")
	}
	if secret.Mode == storage.CredentialModePassword {
		return nil
	}
	if secret.Mode != storage.CredentialModeToken {
		return errors.New("凭据模式无效")
	}
	switch siteType {
	case storage.UpstreamTypeNewAPI:
		var credential account.NewAPITokenCredential
		if err := json.Unmarshal([]byte(secret.Value), &credential); err != nil {
			return errors.New("NewAPI token 凭据不是有效 JSON")
		}
		cookie := strings.TrimSpace(credential.Cookie)
		accessToken := strings.TrimSpace(credential.AccessToken)
		if (cookie == "") == (accessToken == "") {
			return errors.New("NewAPI token 凭据必须且只能包含 Cookie 或 access_token 之一")
		}
		if strings.TrimSpace(credential.UserID) == "" {
			return errors.New("NewAPI token 凭据缺少 user_id")
		}
	case storage.UpstreamTypeSub2API:
		var credential account.Sub2APITokenCredential
		if err := json.Unmarshal([]byte(secret.Value), &credential); err != nil {
			return errors.New("Sub2API token 凭据不是有效 JSON")
		}
		if strings.TrimSpace(credential.AccessToken) == "" {
			return errors.New("Sub2API token 凭据缺少 access_token")
		}
	default:
		return errors.New("站点类型无效")
	}
	return nil
}
