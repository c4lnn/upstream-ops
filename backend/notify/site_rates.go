package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
)

// DispatchSiteRateBatch 聚合同一站点同一扫描批次内的倍率变化。
func (d *Dispatcher) DispatchSiteRateBatch(ctx context.Context, site storage.UpstreamSite, changes []SiteRateChange, failures []string) error {
	return d.dispatchSiteRateEventBatch(ctx, site, storage.EventRateChanged, changes, failures)
}

// DispatchSiteRateStructureBatch 聚合同一站点同一扫描批次内的分组新增/删除。
func (d *Dispatcher) DispatchSiteRateStructureBatch(ctx context.Context, site storage.UpstreamSite, changes []SiteRateChange, failures []string) error {
	return d.dispatchSiteRateEventBatch(ctx, site, storage.EventRateStructureChanged, changes, failures)
}

func (d *Dispatcher) dispatchSiteRateEventBatch(ctx context.Context, site storage.UpstreamSite, event storage.NotificationEvent, changes []SiteRateChange, failures []string) error {
	if len(changes) == 0 {
		return nil
	}
	policy := d.Policy()
	filtered := make([]SiteRateChange, 0, len(changes))
	for _, change := range dedupeSiteRateChanges(changes) {
		if event == storage.EventRateChanged && !change.RateChange.ChangePctAbove(policy.MinChangePct) {
			continue
		}
		filtered = append(filtered, change)
	}
	if len(filtered) == 0 {
		return nil
	}
	notifyChannels, err := d.repo.ListEnabledChannels()
	if err != nil {
		return err
	}
	if len(notifyChannels) == 0 {
		return nil
	}
	eventKey := siteRateEventKey(site.ID, event, filtered)
	if !d.claimRateEvent(eventKey) {
		return nil
	}
	var errs []error
	for i := range notifyChannels {
		nch := notifyChannels[i]
		subs, _ := ParseSubscriptions(nch.Subscriptions)
		matching := filterSiteChanges(site.ID, event, filtered, subs)
		if len(matching) == 0 {
			continue
		}
		msg := BuildSiteRateMessage(site, event, matching, failures)
		msg.ScanRunID = matching[0].ScanRunID
		msg.EventKey = eventKey
		if err := d.sendOne(ctx, &nch, msg); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func dedupeSiteRateChanges(changes []SiteRateChange) []SiteRateChange {
	out := make([]SiteRateChange, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		key := siteRateChangeSignature(change)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, change)
	}
	return out
}

func (d *Dispatcher) claimRateEvent(key string) bool {
	if strings.TrimSpace(key) == "" {
		return true
	}
	d.rateEventMu.Lock()
	defer d.rateEventMu.Unlock()
	if _, exists := d.rateEventKeys[key]; exists {
		return false
	}
	d.rateEventKeys[key] = struct{}{}
	return true
}

func filterSiteChanges(siteID uint, event storage.NotificationEvent, changes []SiteRateChange, subs []Subscription) []SiteRateChange {
	if len(subs) == 0 {
		return changes
	}
	out := make([]SiteRateChange, 0, len(changes))
	for _, change := range changes {
		matched := false
		for _, sub := range subs {
			stub := Message{
				Event:      event,
				SiteID:     siteID,
				AccountID:  change.AccountID,
				AccountIDs: []uint{change.AccountID},
				ModelName:  change.GroupName,
			}
			if sub.Matches(stub) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, change)
		}
	}
	return out
}

func BuildSiteRateMessage(site storage.UpstreamSite, event storage.NotificationEvent, changes []SiteRateChange, failures []string) Message {
	if len(changes) == 0 {
		return Message{}
	}
	accounts := make([]uint, 0, len(changes))
	seenAccounts := make(map[uint]struct{})
	for _, change := range changes {
		if _, ok := seenAccounts[change.AccountID]; !ok {
			seenAccounts[change.AccountID] = struct{}{}
			accounts = append(accounts, change.AccountID)
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i] < accounts[j] })

	groups := aggregateSiteChanges(changes)
	var body strings.Builder
	fmt.Fprintf(&body, "站点：%s\n", site.Name)
	if event == storage.EventRateChanged {
		fmt.Fprintf(&body, "倍率变化 %d 项：\n", len(groups))
	} else {
		fmt.Fprintf(&body, "分组变动 %d 项：\n", len(groups))
	}
	for _, group := range groups {
		change := group.Sample
		accountNames := strings.Join(group.AccountNames, "、")
		switch change.ChangeType {
		case "added":
			fmt.Fprintf(&body, "  · %s：新增，倍率 %g；账号：%s\n", change.GroupName, change.NewRatio, accountNames)
		case "removed":
			fmt.Fprintf(&body, "  · %s：删除，原倍率 %g；账号：%s\n", change.GroupName, change.OldRatio, accountNames)
		default:
			fmt.Fprintf(&body, "  · %s：%g %s至 %g；账号：%s\n", change.GroupName, change.OldRatio, arrowFor(change.OldRatio, change.NewRatio), change.NewRatio, accountNames)
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(&body, "\n未完成扫描账号：%s", strings.Join(failures, "、"))
	}
	fmt.Fprintf(&body, "\n时间：%s", time.Now().Format("2006-01-02 15:04"))

	subject := "【倍率变化提醒】"
	if event == storage.EventRateStructureChanged {
		subject = "【分组变动通知】"
	}
	return Message{
		Event:      event,
		SiteID:     site.ID,
		AccountIDs: accounts,
		Subject:    fmt.Sprintf("%s%s · %d 项", subject, site.Name, len(groups)),
		Body:       body.String(),
		Extra:      map[string]any{"site_id": site.ID, "account_ids": accounts, "partial": len(failures) > 0},
		Partial:    len(failures) > 0,
	}
}

type siteChangeGroup struct {
	Sample       SiteRateChange
	AccountNames []string
}

func aggregateSiteChanges(changes []SiteRateChange) []siteChangeGroup {
	groups := make([]siteChangeGroup, 0, len(changes))
	indexes := make(map[string]int, len(changes))
	for _, change := range changes {
		key := fmt.Sprintf("%s|%s|%g|%g|%g|%g", change.StableKey, change.ChangeType, change.OldRatio, change.NewRatio, change.OldComp, change.NewComp)
		accountName := strings.TrimSpace(change.AccountName)
		if accountName == "" {
			accountName = fmt.Sprintf("账号 #%d", change.AccountID)
		}
		if index, ok := indexes[key]; ok {
			groups[index].AccountNames = append(groups[index].AccountNames, accountName)
			continue
		}
		indexes[key] = len(groups)
		groups = append(groups, siteChangeGroup{Sample: change, AccountNames: []string{accountName}})
	}
	for i := range groups {
		sort.Strings(groups[i].AccountNames)
	}
	return groups
}

func siteRateEventKey(siteID uint, event storage.NotificationEvent, changes []SiteRateChange) string {
	items := make([]string, 0, len(changes))
	for _, change := range changes {
		items = append(items, siteRateChangeSignature(change))
	}
	sort.Strings(items)
	scanRunID := ""
	if len(changes) > 0 {
		scanRunID = changes[0].ScanRunID
	}
	return fmt.Sprintf("%d:%s:%s:%v", siteID, scanRunID, event, items)
}

func siteRateChangeSignature(change SiteRateChange) string {
	return fmt.Sprintf(
		"%s|%d|%s|%s|%g|%g|%g|%g",
		change.ScanRunID,
		change.AccountID,
		change.StableKey,
		change.ChangeType,
		change.OldRatio,
		change.NewRatio,
		change.OldComp,
		change.NewComp,
	)
}
