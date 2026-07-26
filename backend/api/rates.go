package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/gin-gonic/gin"
)

// rateChangeGroupAccount 聚合倍率变化中的一个成员账号。
// 账号已删除时别名为空，前端回退为 ID 展示。
type rateChangeGroupAccount struct {
	AccountID    uint   `json:"account_id"`
	AccountAlias string `json:"account_alias,omitempty"`
}

// rateChangeGroupItem 一条按扫描批次聚合的倍率变化。
// ID 取组内代表行（最大 id），仅用于前端列表 key；ChangedAt 取组内最大变化时间。
type rateChangeGroupItem struct {
	ID                 uint                     `json:"id"`
	SiteID             uint                     `json:"site_id"`
	SiteName           string                   `json:"site_name,omitempty"`
	ScanRunID          string                   `json:"scan_run_id,omitempty"`
	StableGroupKey     string                   `json:"stable_group_key,omitempty"`
	ChangeType         string                   `json:"change_type,omitempty"`
	ModelName          string                   `json:"model_name"`
	OldRatio           *float64                 `json:"old_ratio,omitempty"`
	NewRatio           float64                  `json:"new_ratio"`
	OldCompletionRatio *float64                 `json:"old_completion_ratio,omitempty"`
	NewCompletionRatio float64                  `json:"new_completion_ratio"`
	ChangedAt          time.Time                `json:"changed_at"`
	Accounts           []rateChangeGroupAccount `json:"accounts"`
}

func decorateRateChangeGroups(d *Deps, groups []storage.RateChangeGroup) ([]rateChangeGroupItem, error) {
	items := make([]rateChangeGroupItem, 0, len(groups))
	if len(groups) == 0 {
		return items, nil
	}
	siteNames := make(map[uint]string)
	if d.Sites != nil {
		sites, err := d.Sites.List()
		if err != nil {
			return nil, err
		}
		for _, site := range sites {
			siteNames[site.ID] = site.Name
		}
	}
	accountAliases := make(map[uint]string)
	if d.Accounts != nil {
		accounts, err := d.Accounts.List()
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			accountAliases[account.ID] = account.Alias
		}
	}
	for _, group := range groups {
		latest := group.Latest
		item := rateChangeGroupItem{
			ID:                 latest.ID,
			SiteID:             latest.SiteID,
			SiteName:           siteNames[latest.SiteID],
			ScanRunID:          latest.ScanRunID,
			StableGroupKey:     latest.StableGroupKey,
			ChangeType:         latest.ChangeType,
			ModelName:          latest.ModelName,
			OldRatio:           latest.OldRatio,
			NewRatio:           latest.NewRatio,
			OldCompletionRatio: latest.OldCompletionRatio,
			NewCompletionRatio: latest.NewCompletionRatio,
			ChangedAt:          group.ChangedAt,
			Accounts:           make([]rateChangeGroupAccount, 0, len(group.Members)),
		}
		if item.ChangedAt.IsZero() {
			item.ChangedAt = latest.ChangedAt
		}
		for _, member := range group.Members {
			item.Accounts = append(item.Accounts, rateChangeGroupAccount{
				AccountID:    member.AccountID,
				AccountAlias: accountAliases[member.AccountID],
			})
		}
		items = append(items, item)
	}
	return items, nil
}

func registerRates(g *gin.RouterGroup, d *Deps) {
	g.GET("/rate-changes", func(c *gin.Context) {
		var accountID uint
		if s := c.Query("account_id"); s != "" {
			id, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, err)
				return
			}
			accountID = uint(id)
		}
		page, pageSize, err := parsePageQuery(c)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		groups, total, err := d.Rates.ListChangeGroupsPage(accountID, page, pageSize)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		items, err := decorateRateChangeGroups(d, groups)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		pages := 1
		if total > 0 {
			pages = int((total + int64(pageSize) - 1) / int64(pageSize))
		}
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"pages":     pages,
		}})
	})
}
