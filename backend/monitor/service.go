// Package monitor 周期性扫描上游账号，采集余额 / 倍率并写入快照、变化日志和通知。
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bejix/upstream-ops/backend/account"
	"github.com/bejix/upstream-ops/backend/connector"
	"github.com/bejix/upstream-ops/backend/notify"
	"github.com/bejix/upstream-ops/backend/progress"
	"github.com/bejix/upstream-ops/backend/storage"
)

// Service 监控扫描服务。
type Service struct {
	accounts      *storage.UpstreamAccounts
	sites         *storage.UpstreamSites
	announcements *storage.UpstreamAnnouncements
	rates         *storage.Rates
	monitorLogs   *storage.MonitorLogs
	accountSvc    *account.Service
	dispatcher    *notify.Dispatcher
	log           *slog.Logger
}

var rateScanSequence atomic.Uint64

func (s *Service) SetSites(sites *storage.UpstreamSites) {
	s.sites = sites
}

func NewService(
	accounts *storage.UpstreamAccounts,
	announcements *storage.UpstreamAnnouncements,
	rates *storage.Rates,
	monitorLogs *storage.MonitorLogs,
	accountSvc *account.Service,
	dispatcher *notify.Dispatcher,
	log *slog.Logger,
) *Service {
	return &Service{
		accounts:      accounts,
		announcements: announcements,
		rates:         rates,
		monitorLogs:   monitorLogs,
		accountSvc:    accountSvc,
		dispatcher:    dispatcher,
		log:           log,
	}
}

// ScanAllBalances scans all enabled accounts independently. One account failure
// never redirects work to another account.
func (s *Service) ScanAllBalances(ctx context.Context) {
	if s.scanStopped(ctx, "balance") {
		return
	}
	list, err := s.accounts.ListMonitorEnabled()
	if err != nil {
		s.log.Error("list accounts", "err", err)
		return
	}
	for i := range list {
		if s.scanStopped(ctx, "balance") {
			return
		}
		c := list[i]
		if err := s.RefreshBalance(ctx, &c); err != nil {
			s.log.Warn("refresh balance failed", "account", c.Alias, "err", err)
			if s.scanStopped(ctx, "balance") {
				return
			}
			continue
		}
		if s.scanStopped(ctx, "balance") {
			return
		}
		if err := s.CheckSubscriptionUsageAlerts(ctx, &c); err != nil {
			s.log.Warn("check subscription usage failed", "account", c.Alias, "err", err)
			if s.scanStopped(ctx, "balance") {
				return
			}
		}
	}
}

// ScanAllRates scans accounts grouped by site so rate notifications can be
// aggregated after every account has completed independently.
func (s *Service) ScanAllRates(ctx context.Context) {
	if s.scanStopped(ctx, "rates") {
		return
	}
	if s.sites == nil {
		s.log.Error("site repository is not configured")
		return
	}
	s.ScanAllSiteRates(ctx)
}

// ScanAllSiteRates 按站点串行扫描账号，并在站点批次结束后统一派发通知。
func (s *Service) ScanAllSiteRates(ctx context.Context) {
	if s.scanStopped(ctx, "rates") {
		return
	}
	sites, err := s.sites.List()
	if err != nil {
		s.log.Error("list upstream sites", "err", err)
		return
	}
	for _, site := range sites {
		if s.scanStopped(ctx, "rates") {
			return
		}
		accounts, err := s.sites.ListEnabledAccounts(site.ID)
		if err != nil {
			s.log.Warn("list site accounts", "site", site.Name, "err", err)
			continue
		}
		scanRunID := newRateScanRunID()
		var rateChanges, structureChanges []notify.SiteRateChange
		failures := make([]string, 0)
		for i := range accounts {
			if s.scanStopped(ctx, "rates") {
				return
			}
			account := accounts[i]
			result, scanErr := s.collectRateChanges(ctx, &account, scanRunID)
			if s.scanStopped(ctx, "rates") {
				return
			}
			if scanErr != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", account.Alias, scanErr))
				continue
			}
			rateChanges = append(rateChanges, result.RateChanges...)
			structureChanges = append(structureChanges, result.StructureChanges...)
		}
		if s.scanStopped(ctx, "rates") {
			return
		}
		if len(rateChanges) > 0 {
			_ = s.dispatcher.DispatchSiteRateBatch(ctx, site, rateChanges, failures)
		}
		if len(structureChanges) > 0 {
			_ = s.dispatcher.DispatchSiteRateStructureBatch(ctx, site, structureChanges, failures)
		}
		if s.scanStopped(ctx, "rates") {
			return
		}
		if err := s.syncSiteAnnouncements(ctx, &site); err != nil {
			s.log.Warn("sync site announcements failed", "site", site.Name, "err", err)
		}
	}
}

// RefreshBalance 单个账号余额刷新，可被 API 手动触发。
func (s *Service) RefreshBalance(ctx context.Context, c *storage.UpstreamAccount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return err
	}

	progress.Start(ctx, progress.StageBalance, "拉取余额…")
	started := time.Now()
	res, err := conn.GetBalance(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		AccountID:    c.ID,
		Job:          storage.MonitorJobBalance,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageBalance, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "余额采集失败", err)
		return err
	}

	sampledAt := res.SampledAt
	if sampledAt.IsZero() {
		sampledAt = time.Now()
	}
	if err := s.accounts.UpdateBalance(c.ID, res.Balance, &sampledAt, ""); err != nil {
		return err
	}
	_ = s.rates.AppendBalance(&storage.BalanceSnapshot{
		AccountID: c.ID,
		Balance:   res.Balance,
		SampledAt: sampledAt,
	})
	progress.OK(ctx, progress.StageBalance, fmt.Sprintf("当前余额 %.4f", res.Balance),
		map[string]any{"balance": res.Balance})
	if err := ctx.Err(); err != nil {
		return err
	}

	progress.Start(ctx, progress.StageCost, "拉取消费…")
	costRes, err := conn.GetCosts(ctx, resolved, session)
	if err != nil {
		progress.Fail(ctx, progress.StageCost, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "消费采集失败", err)
		return err
	}
	if err := s.accounts.UpdateCosts(c.ID, costRes.TodayCost, costRes.TotalCost); err != nil {
		progress.Fail(ctx, progress.StageCost, err.Error())
		return err
	}
	_ = s.rates.AppendCost(&storage.CostSnapshot{
		AccountID: c.ID,
		TodayCost: costRes.TodayCost,
		SampledAt: sampledAt,
	})
	progress.OK(ctx, progress.StageCost, fmt.Sprintf("今日 %0.4f / 累计 %0.4f", costRes.TodayCost, costRes.TotalCost),
		map[string]any{"today_cost": costRes.TodayCost, "total_cost": costRes.TotalCost})

	if c.BalanceThreshold > 0 && res.Balance < c.BalanceThreshold {
		label := s.accountLabel(c)
		body := fmt.Sprintf("账号：%s\n当前余额: %.4f，阈值: %.4f", label, res.Balance, c.BalanceThreshold)
		_ = s.dispatcher.Dispatch(ctx, notify.Message{
			Event:      storage.EventBalanceLow,
			AccountID:  c.ID,
			SiteID:     c.SiteID,
			AccountIDs: []uint{c.ID},
			Subject:    fmt.Sprintf("%s 余额低于阈值", label),
			Body:       body,
		})
	}
	return nil
}

// RefreshRates 单个账号倍率刷新，可被 API 手动触发。
func (s *Service) RefreshRates(ctx context.Context, c *storage.UpstreamAccount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scanRunID := newRateScanRunID()
	result, err := s.collectRateChanges(ctx, c, scanRunID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.sites == nil {
		return errors.New("站点服务未配置")
	}
	site, err := s.sites.FindByID(c.SiteID)
	if err != nil {
		return err
	}
	if len(result.RateChanges) > 0 {
		_ = s.dispatcher.DispatchSiteRateBatch(ctx, *site, result.RateChanges, nil)
	}
	if len(result.StructureChanges) > 0 {
		_ = s.dispatcher.DispatchSiteRateStructureBatch(ctx, *site, result.StructureChanges, nil)
	}
	if err := s.syncSiteAnnouncements(ctx, site); err != nil {
		s.log.Warn("sync site announcements failed", "site", site.Name, "err", err)
	}
	return nil
}

type rateScanResult struct {
	RateChanges      []notify.SiteRateChange
	StructureChanges []notify.SiteRateChange
}

type SiteAccountSyncResult struct {
	AccountID   uint              `json:"account_id"`
	AccountName string            `json:"account_name"`
	Success     bool              `json:"success"`
	Error       string            `json:"error,omitempty"`
	Stages      []SyncStageResult `json:"stages,omitempty"`
}

type SyncStageResult struct {
	Stage   progress.Stage `json:"stage"`
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
}

type SyncSummary struct {
	Status       string                  `json:"status"`
	SuccessCount int                     `json:"success_count"`
	FailedCount  int                     `json:"failed_count"`
	Items        []SiteAccountSyncResult `json:"items"`
	Error        string                  `json:"error,omitempty"`
}

type stageResultObserver struct {
	base    progress.Observer
	results map[progress.Stage]SyncStageResult
}

func newStageResultObserver(base progress.Observer) *stageResultObserver {
	return &stageResultObserver{base: base, results: make(map[progress.Stage]SyncStageResult)}
}

func (o *stageResultObserver) Emit(ev progress.Event) {
	o.base.Emit(ev)
	if ev.OK == nil || ev.Stage == progress.StageDone || ev.Stage == progress.StageError {
		return
	}
	result := SyncStageResult{Stage: ev.Stage, Success: *ev.OK}
	if !*ev.OK {
		result.Error = ev.Message
	}
	o.results[ev.Stage] = result
}

func (o *stageResultObserver) list() []SyncStageResult {
	order := []progress.Stage{progress.StageBalance, progress.StageCost, progress.StageSubscription, progress.StageRates}
	out := make([]SyncStageResult, 0, len(o.results))
	for _, stage := range order {
		if result, ok := o.results[stage]; ok {
			out = append(out, result)
		}
	}
	return out
}

func NewSyncSummary(items []SiteAccountSyncResult, err error) SyncSummary {
	summary := SyncSummary{Items: items}
	for _, item := range items {
		if item.Success {
			summary.SuccessCount++
		} else {
			summary.FailedCount++
		}
	}
	if err != nil {
		summary.Error = err.Error()
	}
	switch {
	case summary.FailedCount == 0 && err == nil:
		summary.Status = "success"
	case summary.SuccessCount > 0:
		summary.Status = "partial"
	default:
		summary.Status = "failed"
	}
	return summary
}

func (s *Service) SyncAccount(ctx context.Context, accountID uint) ([]SiteAccountSyncResult, error) {
	account, err := s.accounts.FindByID(accountID)
	if err != nil {
		return nil, err
	}
	if s.sites == nil {
		return nil, errors.New("站点服务未配置")
	}
	site, err := s.sites.FindByID(account.SiteID)
	if err != nil {
		return nil, err
	}
	return s.syncSiteBatch(ctx, site, []storage.UpstreamAccount{*account}, 1, 1)
}

// SyncSite 手动刷新站点内全部账号。各账号独立成功或失败，倍率变化在站点批次结束后聚合。
func (s *Service) SyncSite(ctx context.Context, siteID uint) ([]SiteAccountSyncResult, error) {
	if s.sites == nil {
		return nil, errors.New("站点服务未配置")
	}
	site, err := s.sites.FindByID(siteID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.sites.ListAccounts(siteID)
	if err != nil {
		return nil, err
	}
	return s.syncSiteBatch(ctx, site, accounts, 1, 1)
}

func (s *Service) SyncAll(ctx context.Context) ([]SiteAccountSyncResult, error) {
	if s.sites == nil {
		return nil, errors.New("站点服务未配置")
	}
	sites, err := s.sites.List()
	if err != nil {
		return nil, err
	}
	allResults := make([]SiteAccountSyncResult, 0)
	batchErrors := make([]error, 0)
	for index := range sites {
		site := sites[index]
		accounts, listErr := s.sites.ListAccounts(site.ID)
		if listErr != nil {
			batchErrors = append(batchErrors, fmt.Errorf("%s：%w", site.Name, listErr))
			continue
		}
		results, syncErr := s.syncSiteBatch(ctx, &site, accounts, index+1, len(sites))
		allResults = append(allResults, results...)
		if syncErr != nil {
			batchErrors = append(batchErrors, fmt.Errorf("%s：%w", site.Name, syncErr))
		}
	}
	return allResults, errors.Join(batchErrors...)
}

func (s *Service) syncSiteBatch(ctx context.Context, site *storage.UpstreamSite, accounts []storage.UpstreamAccount, siteIndex, siteTotal int) ([]SiteAccountSyncResult, error) {
	siteCtx := progress.WithScope(ctx, progress.Scope{
		Level: "site", SiteID: site.ID, SiteName: site.Name, SiteIndex: siteIndex, SiteTotal: siteTotal,
	})
	scanRunID := newRateScanRunID()
	results := make([]SiteAccountSyncResult, 0, len(accounts))
	var rateChanges, structureChanges []notify.SiteRateChange
	failures := make([]string, 0)
	for i := range accounts {
		account := accounts[i]
		accountCtx := progress.WithScope(siteCtx, progress.Scope{
			Level: "account", AccountID: account.ID, AccountAlias: account.Alias, Index: i + 1, Total: len(accounts),
		})
		recorder := newStageResultObserver(progress.FromContext(accountCtx))
		accountCtx = progress.WithObserver(accountCtx, recorder)
		balanceErr := s.RefreshBalance(accountCtx, &account)
		var subscriptionErr error
		if balanceErr == nil {
			subscriptionErr = s.CheckSubscriptionUsageAlerts(accountCtx, &account)
		}
		rateResult, rateErr := s.collectRateChanges(accountCtx, &account, scanRunID)
		rateChanges = append(rateChanges, rateResult.RateChanges...)
		structureChanges = append(structureChanges, rateResult.StructureChanges...)
		combinedErr := joinDistinctErrors(balanceErr, subscriptionErr, rateErr)
		item := SiteAccountSyncResult{AccountID: account.ID, AccountName: account.Alias, Success: combinedErr == nil, Stages: recorder.list()}
		if combinedErr != nil {
			item.Error = combinedErr.Error()
			failures = append(failures, fmt.Sprintf("%s：%v", account.Alias, combinedErr))
			progress.Fail(accountCtx, progress.StageError, "同步失败："+combinedErr.Error())
		} else {
			progress.OK(accountCtx, progress.StageDone, "同步完成")
		}
		results = append(results, item)
	}
	if len(rateChanges) > 0 {
		_ = s.dispatcher.DispatchSiteRateBatch(siteCtx, *site, rateChanges, failures)
	}
	if len(structureChanges) > 0 {
		_ = s.dispatcher.DispatchSiteRateStructureBatch(siteCtx, *site, structureChanges, failures)
	}
	if err := s.syncSiteAnnouncements(siteCtx, site); err != nil {
		failures = append(failures, "公告："+err.Error())
	}
	batchErr := errors.Join(stringsToErrors(failures)...)
	summary := NewSyncSummary(results, batchErr)
	message := syncSummaryMessage(summary)
	if summary.Status == "success" {
		progress.OK(siteCtx, progress.StageDone, message, summary)
	} else {
		progress.Fail(siteCtx, progress.StageError, message)
	}
	return results, batchErr
}

func syncSummaryMessage(summary SyncSummary) string {
	switch summary.Status {
	case "success":
		return fmt.Sprintf("同步完成 · 成功 %d", summary.SuccessCount)
	case "partial":
		return fmt.Sprintf("部分同步完成 · 成功 %d / 失败 %d", summary.SuccessCount, summary.FailedCount)
	default:
		return fmt.Sprintf("同步失败 · 失败 %d", summary.FailedCount)
	}
}

func stringsToErrors(items []string) []error {
	out := make([]error, 0, len(items))
	for _, item := range items {
		out = append(out, errors.New(item))
	}
	return out
}

func joinDistinctErrors(items ...error) error {
	seen := make(map[string]struct{}, len(items))
	unique := make([]error, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		message := item.Error()
		if _, exists := seen[message]; exists {
			continue
		}
		seen[message] = struct{}{}
		unique = append(unique, item)
	}
	return errors.Join(unique...)
}

func newRateScanRunID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(rateScanSequence.Add(1), 36)
}

func (s *Service) collectRateChanges(ctx context.Context, c *storage.UpstreamAccount, scanRunID string) (rateScanResult, error) {
	var output rateScanResult
	if err := ctx.Err(); err != nil {
		return output, err
	}
	resolved, conn, session, err := s.prepare(ctx, c)
	if err != nil {
		s.notifyError(ctx, c, storage.EventLoginFailed, "登录失败", err)
		return output, err
	}

	progress.Start(ctx, progress.StageRates, "拉取分组倍率…")
	started := time.Now()
	results, err := conn.GetRates(ctx, resolved, session)
	finished := time.Now()
	_ = s.monitorLogs.Append(&storage.MonitorLog{
		AccountID:    c.ID,
		Job:          storage.MonitorJobRates,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageRates, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "倍率采集失败", err)
		return output, err
	}

	now := time.Now()
	existing, err := s.rates.ListByAccount(c.ID)
	if err != nil {
		return output, err
	}
	isFirstSync := len(existing) == 0
	existingByKey := make(map[string]storage.RateSnapshot, len(existing))
	for _, snapshot := range existing {
		key := snapshot.StableGroupKey
		if key == "" {
			key = storage.StableRateGroupKey(snapshot.RemoteGroupID, snapshot.ModelName)
		}
		existingByKey[key] = snapshot
	}
	seen := make(map[string]struct{}, len(results))
	for _, r := range results {
		stableKey := storage.StableRateGroupKey(r.GroupID, r.ModelName)
		seen[stableKey] = struct{}{}
		prev, err := s.rates.Upsert(&storage.RateSnapshot{
			AccountID:       c.ID,
			StableGroupKey:  stableKey,
			RemoteGroupID:   r.GroupID,
			ModelName:       r.ModelName,
			Description:     r.Description,
			Ratio:           r.Ratio,
			CompletionRatio: r.CompletionRatio,
			LastSeenAt:      now,
		})
		if err != nil {
			s.log.Warn("rate upsert failed", "account", c.Alias, "model", r.ModelName, "err", err)
			continue
		}
		if prev == nil {
			if !isFirstSync {
				change := siteRateChange(c, scanRunID, stableKey, "added", notify.RateChange{GroupName: r.ModelName, NewRatio: r.Ratio, NewComp: r.CompletionRatio, ChangedAt: now})
				output.StructureChanges = append(output.StructureChanges, change)
				_ = s.rates.AppendChange(&storage.RateChangeLog{
					SiteID: c.SiteID, AccountID: c.ID, ScanRunID: scanRunID,
					StableGroupKey: stableKey, ChangeType: "added", ModelName: r.ModelName,
					NewRatio: r.Ratio, NewCompletionRatio: r.CompletionRatio, ChangedAt: now,
				})
			}
			continue
		}
		if prev.Ratio == r.Ratio && prev.CompletionRatio == r.CompletionRatio {
			continue
		}
		oldRatio := prev.Ratio
		oldComp := prev.CompletionRatio
		_ = s.rates.AppendChange(&storage.RateChangeLog{
			SiteID:             c.SiteID,
			AccountID:          c.ID,
			ScanRunID:          scanRunID,
			StableGroupKey:     stableKey,
			ChangeType:         "ratio_changed",
			ModelName:          r.ModelName,
			OldRatio:           &oldRatio,
			NewRatio:           r.Ratio,
			OldCompletionRatio: &oldComp,
			NewCompletionRatio: r.CompletionRatio,
			ChangedAt:          now,
		})
		output.RateChanges = append(output.RateChanges, siteRateChange(c, scanRunID, stableKey, "ratio_changed", notify.RateChange{
			GroupName: r.ModelName,
			OldRatio:  oldRatio,
			NewRatio:  r.Ratio,
			OldComp:   oldComp,
			NewComp:   r.CompletionRatio,
			ChangedAt: now,
		}))
	}
	for stableKey, snapshot := range existingByKey {
		if _, ok := seen[stableKey]; ok {
			continue
		}
		if err := s.rates.DeleteSnapshotByKey(c.ID, stableKey); err != nil {
			s.log.Warn("rate delete failed", "account", c.Alias, "model", snapshot.ModelName, "err", err)
			continue
		}
		change := siteRateChange(c, scanRunID, stableKey, "removed", notify.RateChange{
			GroupName: snapshot.ModelName,
			OldRatio:  snapshot.Ratio,
			OldComp:   snapshot.CompletionRatio,
			ChangedAt: now,
		})
		output.StructureChanges = append(output.StructureChanges, change)
		oldRatio := snapshot.Ratio
		oldComp := snapshot.CompletionRatio
		_ = s.rates.AppendChange(&storage.RateChangeLog{
			SiteID: c.SiteID, AccountID: c.ID, ScanRunID: scanRunID,
			StableGroupKey: stableKey, ChangeType: "removed", ModelName: snapshot.ModelName,
			OldRatio: &oldRatio, OldCompletionRatio: &oldComp, ChangedAt: now,
		})
	}
	progress.OK(ctx, progress.StageRates, fmt.Sprintf("拉到 %d 个分组", len(results)),
		map[string]any{"count": len(results), "scan_run_id": scanRunID})
	return output, nil
}

func siteRateChange(c *storage.UpstreamAccount, scanRunID, stableKey, changeType string, change notify.RateChange) notify.SiteRateChange {
	return notify.SiteRateChange{
		SiteID: c.SiteID, AccountID: c.ID, AccountName: c.Alias,
		ScanRunID: scanRunID, StableKey: stableKey, ChangeType: changeType, RateChange: change,
	}
}

func (s *Service) CheckSubscriptionUsageAlerts(ctx context.Context, c *storage.UpstreamAccount) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || !c.MonitorEnabled || !c.SubscriptionEnabled {
		return nil
	}
	if s.sites == nil {
		return errors.New("站点服务未配置")
	}
	site, err := s.sites.FindByID(c.SiteID)
	if err != nil {
		return err
	}
	if site.Type != storage.UpstreamTypeSub2API {
		return nil
	}
	policy := s.dispatcher.Policy()
	if policy.SubscriptionDailyRemainingThresholdPct <= 0 &&
		policy.SubscriptionWeeklyRemainingThresholdPct <= 0 &&
		policy.SubscriptionMonthlyRemainingThresholdPct <= 0 &&
		policy.SubscriptionExpiryThreshold <= 0 {
		return nil
	}
	info, err := s.accountSvc.GetSubscriptionUsage(ctx, c.ID)
	if err != nil {
		progress.Fail(ctx, progress.StageSubscription, err.Error())
		s.notifyError(ctx, c, storage.EventMonitorFailed, "订阅用量采集失败", err)
		return err
	}
	s.dispatchSubscriptionWindowAlert(ctx, c, storage.EventSubscriptionDailyLow, "每日", policy.SubscriptionDailyRemainingThresholdPct, info.Items, func(item connector.SubscriptionUsage) *connector.SubscriptionUsageWindow {
		return item.Daily
	})
	s.dispatchSubscriptionWindowAlert(ctx, c, storage.EventSubscriptionWeeklyLow, "每周", policy.SubscriptionWeeklyRemainingThresholdPct, info.Items, func(item connector.SubscriptionUsage) *connector.SubscriptionUsageWindow {
		return item.Weekly
	})
	s.dispatchSubscriptionWindowAlert(ctx, c, storage.EventSubscriptionMonthlyLow, "每月", policy.SubscriptionMonthlyRemainingThresholdPct, info.Items, func(item connector.SubscriptionUsage) *connector.SubscriptionUsageWindow {
		return item.Monthly
	})
	s.dispatchSubscriptionExpiryAlert(ctx, c, policy.SubscriptionExpiryThreshold, info.Items)
	progress.OK(ctx, progress.StageSubscription, fmt.Sprintf("检查订阅用量 %d 项", len(info.Items)),
		map[string]any{"count": len(info.Items)})
	return nil
}

func (s *Service) dispatchSubscriptionWindowAlert(ctx context.Context, c *storage.UpstreamAccount, event storage.NotificationEvent, label string, threshold float64, items []connector.SubscriptionUsage, pick func(connector.SubscriptionUsage) *connector.SubscriptionUsageWindow) {
	if threshold <= 0 {
		return
	}
	lines := make([]string, 0)
	for _, item := range items {
		w := pick(item)
		if w == nil || w.LimitUSD <= 0 || w.RemainingPercent > threshold {
			continue
		}
		reset := "—"
		if w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			reset = w.ResetsAt.Format("01-02 15:04")
		}
		lines = append(lines, fmt.Sprintf("· %s：已用 $%.4f / $%.4f，剩余 $%.4f（%.1f%%），重置 %s",
			subscriptionGroupName(item), w.UsedUSD, w.LimitUSD, w.RemainingUSD, w.RemainingPercent, reset))
	}
	if len(lines) == 0 {
		return
	}
	accountName := s.accountLabel(c)
	body := fmt.Sprintf("账号：%s\n维度：%s\n阈值：剩余 %.1f%%\n%s", accountName, label, threshold, strings.Join(lines, "\n"))
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:      event,
		AccountID:  c.ID,
		SiteID:     c.SiteID,
		AccountIDs: []uint{c.ID},
		Subject:    fmt.Sprintf("%s 订阅%s剩余额度不足", accountName, label),
		Body:       body,
	})
}

func (s *Service) dispatchSubscriptionExpiryAlert(ctx context.Context, c *storage.UpstreamAccount, threshold time.Duration, items []connector.SubscriptionUsage) {
	if threshold <= 0 {
		return
	}
	now := time.Now()
	lines := make([]string, 0)
	for _, item := range items {
		if item.ExpiresAt == nil || item.ExpiresAt.IsZero() {
			continue
		}
		remaining := item.ExpiresAt.Sub(now)
		if remaining > threshold {
			continue
		}
		lines = append(lines, fmt.Sprintf("· %s：到期 %s，剩余 %s",
			subscriptionGroupName(item), item.ExpiresAt.Format("2006-01-02 15:04"), formatDurationHours(remaining)))
	}
	if len(lines) == 0 {
		return
	}
	accountName := s.accountLabel(c)
	body := fmt.Sprintf("账号：%s\n类型：订阅即将到期\n阈值：剩余 %.0f 小时\n%s", accountName, threshold.Hours(), strings.Join(lines, "\n"))
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:      storage.EventSubscriptionExpiring,
		AccountID:  c.ID,
		SiteID:     c.SiteID,
		AccountIDs: []uint{c.ID},
		Subject:    fmt.Sprintf("%s 订阅即将到期", accountName),
		Body:       body,
	})
}

func subscriptionGroupName(item connector.SubscriptionUsage) string {
	if strings.TrimSpace(item.GroupName) != "" {
		return strings.TrimSpace(item.GroupName)
	}
	if item.GroupID > 0 {
		return fmt.Sprintf("分组 %d", item.GroupID)
	}
	return fmt.Sprintf("订阅 %d", item.ID)
}

func formatDurationHours(d time.Duration) string {
	if d <= 0 {
		return "已到期"
	}
	hours := d.Hours()
	if hours < 1 {
		return fmt.Sprintf("%.0f 分钟", d.Minutes())
	}
	return fmt.Sprintf("%.1f 小时", hours)
}

func (s *Service) prepare(ctx context.Context, c *storage.UpstreamAccount) (*connector.AccountTarget, connector.Connector, *connector.AuthSession, error) {
	resolved, err := s.accountSvc.Resolve(ctx, c)
	if err != nil {
		return nil, nil, nil, err
	}
	conn, err := connector.For(resolved.Type)
	if err != nil {
		return nil, nil, nil, err
	}
	s.accountSvc.ApplyHTTPConfig(conn)
	s.accountSvc.ApplyProxy(conn, resolved)
	session, err := s.accountSvc.EnsureSession(ctx, c, resolved, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, conn, session, nil
}

func (s *Service) notifyError(ctx context.Context, c *storage.UpstreamAccount, event storage.NotificationEvent, subject string, err error) {
	_ = s.dispatcher.Dispatch(ctx, notify.Message{
		Event:      event,
		AccountID:  c.ID,
		SiteID:     c.SiteID,
		AccountIDs: []uint{c.ID},
		Subject:    fmt.Sprintf("%s %s", s.accountLabel(c), subject),
		Body:       err.Error(),
	})
}

// accountLabel 返回账号级通知使用的 "站点名称 / 账号别名"，
// 保证跨站点同名账号在告警里可区分；站点解析失败时退化为账号别名。
func (s *Service) accountLabel(c *storage.UpstreamAccount) string {
	if s.sites != nil {
		if site, err := s.sites.FindByID(c.SiteID); err == nil && site != nil && site.Name != "" {
			return site.Name + " / " + c.Alias
		}
	}
	return c.Alias
}

func (s *Service) syncSiteAnnouncements(ctx context.Context, site *storage.UpstreamSite) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.announcements == nil || s.sites == nil || site == nil || site.IgnoreAnnouncements {
		return nil
	}
	if site.DefaultAccountID == 0 {
		return errors.New("站点没有默认账号，无法读取公告")
	}
	account, err := s.accounts.FindByID(site.DefaultAccountID)
	if err != nil {
		return err
	}
	if account.SiteID != site.ID {
		return errors.New("默认账号不属于当前站点")
	}
	if !account.MonitorEnabled {
		return errors.New("默认账号监控已暂停，无法读取公告")
	}
	resolved, conn, session, err := s.prepare(ctx, account)
	if err != nil {
		return fmt.Errorf("默认账号读取站点公告失败: %w", err)
	}
	items, err := conn.GetAnnouncements(ctx, resolved, session)
	if err != nil {
		return fmt.Errorf("读取站点公告失败: %w", err)
	}
	return s.storeAnnouncements(ctx, site.ID, site.Name, account, items)
}

func (s *Service) scanStopped(ctx context.Context, task string) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	if s.log != nil {
		s.log.Info("monitor scan stopped because context is done", "task", task, "err", ctx.Err())
	}
	return true
}

func (s *Service) storeAnnouncements(ctx context.Context, siteID uint, siteName string, c *storage.UpstreamAccount, items []connector.AnnouncementResult) error {
	if len(items) == 0 {
		return nil
	}
	records := make([]storage.UpstreamAnnouncement, 0, len(items))
	for _, item := range items {
		records = append(records, storage.UpstreamAnnouncement{
			SiteID:          siteID,
			AccountID:       c.ID,
			SourceKey:       item.SourceKey,
			Title:           item.Title,
			Content:         item.Content,
			Type:            item.Type,
			Link:            item.Link,
			PublishedAt:     item.PublishedAt,
			SourceUpdatedAt: item.SourceUpdatedAt,
		})
	}
	existingCount, err := s.announcements.CountBySite(siteID)
	if err != nil {
		return err
	}
	newRecords, err := s.announcements.SyncSite(siteID, c.ID, records)
	if err != nil {
		return err
	}
	if existingCount == 0 {
		return nil
	}
	for i := range newRecords {
		rec := newRecords[i]
		_ = s.dispatcher.Dispatch(ctx, notify.Message{
			Event:      storage.EventAnnouncement,
			AccountID:  c.ID,
			SiteID:     siteID,
			AccountIDs: []uint{c.ID},
			Subject:    announcementSubject(siteName, rec),
			Body:       announcementBody(rec),
			Extra: map[string]any{
				"announcement_id":   rec.ID,
				"source_key":        rec.SourceKey,
				"title":             rec.Title,
				"type":              rec.Type,
				"link":              rec.Link,
				"source_account_id": c.ID,
			},
		})
	}
	return nil
}

func announcementSubject(siteName string, a storage.UpstreamAnnouncement) string {
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = strings.TrimSpace(a.Content)
	}
	if title == "" {
		title = "上游公告"
	}
	if len([]rune(title)) > 40 {
		title = string([]rune(title)[:40])
	}
	return fmt.Sprintf("%s 公告 · %s", siteName, title)
}

func announcementBody(a storage.UpstreamAnnouncement) string {
	var b strings.Builder
	if a.Content != "" {
		b.WriteString(a.Content)
	}
	if a.Link != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("来源：")
		b.WriteString(a.Link)
	}
	if a.PublishedAt != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("发布时间：")
		b.WriteString(a.PublishedAt.Format("2006-01-02 15:04"))
	}
	return b.String()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
