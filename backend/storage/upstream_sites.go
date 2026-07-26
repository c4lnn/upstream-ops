package storage

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

type UpstreamSites struct{ db *gorm.DB }

func NewUpstreamSites(db *gorm.DB) *UpstreamSites { return &UpstreamSites{db: db} }

func (r *UpstreamSites) WithDB(db *gorm.DB) *UpstreamSites { return NewUpstreamSites(db) }

type UpstreamSiteSummary struct {
	UpstreamSite
	Accounts          []UpstreamAccount `json:"accounts"`
	AccountCount      int               `json:"account_count"`
	UncollectedCount  int               `json:"uncollected_count"`
	ErrorAccountCount int               `json:"error_account_count"`
	TotalBalance      *float64          `json:"total_balance,omitempty"`
	TodayCost         *float64          `json:"today_cost,omitempty"`
	LowestBalance     *float64          `json:"lowest_balance,omitempty"`
	LowestAccountID   uint              `json:"lowest_account_id,omitempty"`
	LowestAccountName string            `json:"lowest_account_name,omitempty"`
	LastSyncAt        *time.Time        `json:"last_sync_at,omitempty"`
}

func NormalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("站点入口地址无效: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("站点入口地址必须使用 http 或 https")
	}
	if parsed.Host == "" {
		return "", errors.New("站点入口地址缺少主机名")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("站点入口地址不能包含用户信息、查询参数或片段")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validUpstreamType(value UpstreamType) bool {
	return value == UpstreamTypeNewAPI || value == UpstreamTypeSub2API
}

func (r *UpstreamSites) Create(site *UpstreamSite) error {
	site.Name = strings.TrimSpace(site.Name)
	if site.Name == "" {
		return errors.New("站点名称不能为空")
	}
	if !validUpstreamType(site.Type) {
		return fmt.Errorf("不支持的站点类型: %s", site.Type)
	}
	baseURL, err := NormalizeBaseURL(site.BaseURL)
	if err != nil {
		return err
	}
	site.BaseURL = baseURL
	if site.SortOrder == 0 {
		site.SortOrder = 1
	}
	return r.db.Create(site).Error
}

func (r *UpstreamSites) Update(site *UpstreamSite) error {
	site.Name = strings.TrimSpace(site.Name)
	if site.Name == "" {
		return errors.New("站点名称不能为空")
	}
	if !validUpstreamType(site.Type) {
		return fmt.Errorf("不支持的站点类型: %s", site.Type)
	}
	baseURL, err := NormalizeBaseURL(site.BaseURL)
	if err != nil {
		return err
	}
	site.BaseURL = baseURL
	return r.db.Save(site).Error
}

// UpdateBaseURL replaces a site endpoint after explicit caller confirmation.
// Existing sessions cannot safely survive a destination change, and monitoring
// stays paused until accounts are explicitly tested again.
func (r *UpstreamSites) UpdateBaseURL(siteID uint, rawBaseURL string, confirmed bool) (int, error) {
	if !confirmed {
		return 0, errors.New("修改站点入口必须明确确认")
	}
	baseURL, err := NormalizeBaseURL(rawBaseURL)
	if err != nil {
		return 0, err
	}
	var affected int64
	err = r.db.Transaction(func(tx *gorm.DB) error {
		var site UpstreamSite
		if err := tx.First(&site, siteID).Error; err != nil {
			return err
		}
		if site.BaseURL == baseURL {
			return nil
		}
		if err := tx.Model(&site).Update("base_url", baseURL).Error; err != nil {
			return err
		}
		if err := tx.Model(&UpstreamAccount{}).Where("site_id = ?", siteID).Count(&affected).Error; err != nil {
			return err
		}
		if err := tx.Model(&UpstreamAccount{}).Where("site_id = ?", siteID).Update("monitor_enabled", false).Error; err != nil {
			return err
		}
		var accountIDs []uint
		if err := tx.Model(&UpstreamAccount{}).Where("site_id = ?", siteID).Pluck("id", &accountIDs).Error; err != nil {
			return err
		}
		if len(accountIDs) > 0 {
			if err := tx.Where("account_id IN ?", accountIDs).Delete(&AuthSession{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return int(affected), err
}

func (r *UpstreamSites) FindByID(id uint) (*UpstreamSite, error) {
	var site UpstreamSite
	if err := r.db.First(&site, id).Error; err != nil {
		return nil, err
	}
	return &site, nil
}

func (r *UpstreamSites) List() ([]UpstreamSite, error) {
	var sites []UpstreamSite
	if err := r.db.Order("sort_order DESC").Order("id ASC").Find(&sites).Error; err != nil {
		return nil, err
	}
	return sites, nil
}

func (r *UpstreamSites) ListAccounts(siteID uint) ([]UpstreamAccount, error) {
	var accounts []UpstreamAccount
	err := r.db.Where("site_id = ?", siteID).
		Order("account_sort_order DESC").Order("id ASC").Find(&accounts).Error
	return accounts, err
}

func (r *UpstreamSites) ListEnabledAccounts(siteID uint) ([]UpstreamAccount, error) {
	var accounts []UpstreamAccount
	err := r.db.Where("site_id = ? AND monitor_enabled = ?", siteID, true).
		Order("account_sort_order DESC").Order("id ASC").Find(&accounts).Error
	return accounts, err
}

func (r *UpstreamSites) AddAccount(account *UpstreamAccount) error {
	if account.SiteID == 0 {
		return errors.New("账号必须属于站点")
	}
	account.Alias = strings.TrimSpace(account.Alias)
	if account.Alias == "" {
		return errors.New("账号别名不能为空")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var site UpstreamSite
		if err := tx.First(&site, account.SiteID).Error; err != nil {
			return err
		}
		if account.AccountSortOrder == 0 {
			account.AccountSortOrder = 1
		}
		if err := tx.Create(account).Error; err != nil {
			return err
		}
		if site.DefaultAccountID == 0 {
			return tx.Model(&site).Update("default_account_id", account.ID).Error
		}
		return nil
	})
}

func (r *UpstreamSites) SetDefaultAccount(siteID, accountID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var account UpstreamAccount
		if err := tx.Where("id = ? AND site_id = ?", accountID, siteID).First(&account).Error; err != nil {
			return errors.New("默认账号不属于当前站点")
		}
		return tx.Model(&UpstreamSite{}).Where("id = ?", siteID).Update("default_account_id", accountID).Error
	})
}

func (r *UpstreamSites) DeleteAccount(accountID, replacementID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var account UpstreamAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		var site UpstreamSite
		if err := tx.First(&site, account.SiteID).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&UpstreamAccount{}).Where("site_id = ?", site.ID).Count(&count).Error; err != nil {
			return err
		}
		if site.DefaultAccountID == accountID && count > 1 {
			if replacementID == 0 {
				return errors.New("删除默认账号前必须选择替代账号")
			}
			var replacement UpstreamAccount
			if err := tx.Where("id = ? AND site_id = ? AND id <> ?", replacementID, site.ID, accountID).First(&replacement).Error; err != nil {
				return errors.New("替代账号不属于当前站点")
			}
			if err := tx.Model(&site).Update("default_account_id", replacementID).Error; err != nil {
				return err
			}
		}
		if err := deleteAccountTx(tx, accountID); err != nil {
			return err
		}
		if count == 1 {
			if err := tx.Where("site_id = ?", site.ID).Delete(&UpstreamAnnouncement{}).Error; err != nil {
				return err
			}
			return tx.Delete(&UpstreamSite{}, site.ID).Error
		}
		return nil
	})
}

func (r *UpstreamSites) Delete(siteID uint, cascade bool) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		var accounts []UpstreamAccount
		if err := tx.Where("site_id = ?", siteID).Find(&accounts).Error; err != nil {
			return err
		}
		if len(accounts) > 0 && !cascade {
			return errors.New("站点仍有账号，删除时必须显式确认级联")
		}
		for _, account := range accounts {
			if err := deleteAccountTx(tx, account.ID); err != nil {
				return err
			}
		}
		if err := tx.Where("site_id = ?", siteID).Delete(&UpstreamAnnouncement{}).Error; err != nil {
			return err
		}
		return tx.Delete(&UpstreamSite{}, siteID).Error
	})
}

func (r *UpstreamSites) ListSummaries() ([]UpstreamSiteSummary, error) {
	sites, err := r.List()
	if err != nil {
		return nil, err
	}
	summaries := make([]UpstreamSiteSummary, 0, len(sites))
	for _, site := range sites {
		accounts, err := r.ListAccounts(site.ID)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summarizeSite(site, accounts))
	}
	return summaries, nil
}

func (r *UpstreamSites) Summary(siteID uint) (*UpstreamSiteSummary, error) {
	site, err := r.FindByID(siteID)
	if err != nil {
		return nil, err
	}
	accounts, err := r.ListAccounts(siteID)
	if err != nil {
		return nil, err
	}
	summary := summarizeSite(*site, accounts)
	return &summary, nil
}

func summarizeSite(site UpstreamSite, accounts []UpstreamAccount) UpstreamSiteSummary {
	summary := UpstreamSiteSummary{UpstreamSite: site, Accounts: accounts, AccountCount: len(accounts)}
	var totalBalance, todayCost float64
	var balanceCount, costCount int
	for i := range accounts {
		account := accounts[i]
		if account.LastBalance == nil {
			summary.UncollectedCount++
		} else {
			totalBalance += *account.LastBalance
			balanceCount++
			if summary.LowestBalance == nil || *account.LastBalance < *summary.LowestBalance {
				value := *account.LastBalance
				summary.LowestBalance = &value
				summary.LowestAccountID = account.ID
				summary.LowestAccountName = account.Alias
			}
		}
		if account.TodayCost != nil {
			todayCost += *account.TodayCost
			costCount++
		}
		if strings.TrimSpace(account.LastError) != "" {
			summary.ErrorAccountCount++
		}
		if account.LastBalanceAt != nil && (summary.LastSyncAt == nil || account.LastBalanceAt.After(*summary.LastSyncAt)) {
			value := *account.LastBalanceAt
			summary.LastSyncAt = &value
		}
	}
	if balanceCount > 0 {
		summary.TotalBalance = &totalBalance
	}
	if costCount > 0 {
		summary.TodayCost = &todayCost
	}
	sort.SliceStable(summary.Accounts, func(i, j int) bool {
		if summary.Accounts[i].AccountSortOrder == summary.Accounts[j].AccountSortOrder {
			return summary.Accounts[i].ID < summary.Accounts[j].ID
		}
		return summary.Accounts[i].AccountSortOrder > summary.Accounts[j].AccountSortOrder
	})
	return summary
}
