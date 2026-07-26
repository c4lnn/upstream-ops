package storage

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// UpstreamAccounts persists account-scoped configuration and runtime state.
type UpstreamAccounts struct{ db *gorm.DB }

func NewUpstreamAccounts(db *gorm.DB) *UpstreamAccounts { return &UpstreamAccounts{db: db} }

func (r *UpstreamAccounts) WithDB(db *gorm.DB) *UpstreamAccounts {
	return NewUpstreamAccounts(db)
}

func (r *UpstreamAccounts) Create(account *UpstreamAccount) error {
	if account.SiteID == 0 {
		return errors.New("账号必须属于站点")
	}
	if account.AccountSortOrder == 0 {
		account.AccountSortOrder = 1
	}
	return r.db.Create(account).Error
}

func (r *UpstreamAccounts) Update(account *UpstreamAccount) error { return r.db.Save(account).Error }

func (r *UpstreamAccounts) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return deleteAccountTx(tx, id)
	})
}

func deleteAccountTx(tx *gorm.DB, id uint) error {
	for _, model := range []any{
		&AuthSession{},
		&RateSnapshot{},
		&RateChangeLog{},
		&BalanceSnapshot{},
		&CostSnapshot{},
		&MonitorLog{},
		&NotificationCooldown{},
	} {
		if err := tx.Where("account_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("account_id = ?", id).Delete(&UpstreamAnnouncement{}).Error; err != nil {
		return err
	}
	if err := tx.Where("account_id = ?", id).Delete(&NotificationLog{}).Error; err != nil {
		return err
	}
	var syncAccountIDs []uint
	if err := tx.Model(&UpstreamSyncAccount{}).Where("source_account_id = ?", id).Pluck("id", &syncAccountIDs).Error; err != nil {
		return err
	}
	if len(syncAccountIDs) > 0 {
		if err := tx.Where("sync_account_id IN ?", syncAccountIDs).Delete(&UpstreamSyncManagedAccount{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("source_account_id = ?", id).Delete(&UpstreamSyncAccount{}).Error; err != nil {
		return err
	}
	return tx.Delete(&UpstreamAccount{}, id).Error
}

func (r *UpstreamAccounts) FindByID(id uint) (*UpstreamAccount, error) {
	var account UpstreamAccount
	if err := r.db.First(&account, id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *UpstreamAccounts) List() ([]UpstreamAccount, error) {
	var list []UpstreamAccount
	if err := r.db.Order("account_sort_order DESC").Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *UpstreamAccounts) ListPage(page, pageSize int) ([]UpstreamAccount, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 && pageSize != -1 {
		pageSize = 20
	}
	var total int64
	if err := r.db.Model(&UpstreamAccount{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []UpstreamAccount
	q := r.db.Order("account_sort_order DESC").Order("id ASC")
	if pageSize != -1 {
		q = q.Offset((page - 1) * pageSize).Limit(pageSize)
	}
	if err := q.Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *UpstreamAccounts) ListMonitorEnabled() ([]UpstreamAccount, error) {
	var list []UpstreamAccount
	if err := r.db.Where("monitor_enabled = ?", true).
		Order("account_sort_order DESC").Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *UpstreamAccounts) UpdateBalance(id uint, balance float64, at any, lastErr string) error {
	return r.db.Model(&UpstreamAccount{}).Where("id = ?", id).Updates(map[string]any{
		"last_balance": balance, "last_balance_at": at, "last_error": lastErr,
	}).Error
}

func (r *UpstreamAccounts) UpdateCosts(id uint, todayCost, totalCost float64) error {
	return r.db.Model(&UpstreamAccount{}).Where("id = ?", id).Updates(map[string]any{
		"today_cost": todayCost, "total_cost": totalCost,
	}).Error
}

func (r *UpstreamAccounts) SetLastError(id uint, message string) error {
	return r.db.Model(&UpstreamAccount{}).Where("id = ?", id).Update("last_error", strings.TrimSpace(message)).Error
}
