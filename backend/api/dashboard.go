package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// registerDashboard provides the aggregate data used by the monitoring home.
func registerDashboard(g *gin.RouterGroup, d *Deps) {
	g.GET("/dashboard/summary", func(c *gin.Context) { dashboardSummary(c, d) })
	g.GET("/dashboard/balance-trend", func(c *gin.Context) { dashboardBalanceTrend(c, d) })
	g.GET("/dashboard/cost-trend", func(c *gin.Context) { dashboardCostTrend(c, d) })
}

type dashboardLowest struct {
	SiteID       uint     `json:"site_id"`
	SiteName     string   `json:"site_name"`
	AccountID    uint     `json:"account_id"`
	AccountAlias string   `json:"account_alias"`
	Balance      *float64 `json:"balance"`
}

func dashboardSummary(c *gin.Context, d *Deps) {
	accounts, err := d.Accounts.List()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}

	var totalBalance, todayTotalCost, totalCost float64
	var lowest *dashboardLowest
	var activeCount, failedCount int
	siteNames := make(map[uint]string)
	if d.Sites != nil {
		sites, listErr := d.Sites.List()
		if listErr != nil {
			fail(c, http.StatusInternalServerError, listErr)
			return
		}
		for _, site := range sites {
			siteNames[site.ID] = site.Name
		}
	}

	for _, account := range accounts {
		if account.LastError != "" {
			failedCount++
		} else if account.MonitorEnabled {
			activeCount++
		}
		if account.LastBalance != nil {
			totalBalance += *account.LastBalance
			if lowest == nil || lowest.Balance == nil || *account.LastBalance < *lowest.Balance {
				balance := *account.LastBalance
				lowest = &dashboardLowest{
					SiteID:       account.SiteID,
					SiteName:     siteNames[account.SiteID],
					AccountID:    account.ID,
					AccountAlias: account.Alias,
					Balance:      &balance,
				}
			}
		}
		if account.TodayCost != nil {
			todayTotalCost += *account.TodayCost
		}
		if account.TotalCost != nil {
			totalCost += *account.TotalCost
		}
	}

	recentGroups, _, err := d.Rates.ListChangeGroupsPage(0, 1, 10)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	decoratedChanges, err := decorateRateChangeGroups(d, recentGroups)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"total_sites":         len(siteNames),
			"total_accounts":      len(accounts),
			"active_accounts":     activeCount,
			"failed_accounts":     failedCount,
			"total_balance":       totalBalance,
			"today_total_cost":    todayTotalCost,
			"total_cost":          totalCost,
			"lowest_balance":      lowest,
			"recent_rate_changes": decoratedChanges,
		},
	})
}

func dashboardBalanceTrend(c *gin.Context, d *Deps) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	trend, err := d.Rates.AggregateBalanceTrend(days)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": trend})
}

func dashboardCostTrend(c *gin.Context, d *Deps) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 {
		days = 7
	}
	trend, err := d.Rates.AggregateCostTrend(days)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": trend})
}
