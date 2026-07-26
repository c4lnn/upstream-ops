package storage

import (
	"testing"
	"time"
)

func ptrFloat(v float64) *float64 { return &v }

func seedRateChangeRows(t *testing.T, rates *Rates) time.Time {
	t.Helper()
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	rows := []RateChangeLog{
		// r1 批次：账号 1、2 命中同一变化 → 应合并为一组（全场最新）
		{SiteID: 1, AccountID: 1, ScanRunID: "r1", StableGroupKey: "id:1", ChangeType: "ratio_changed", ModelName: "g", OldRatio: ptrFloat(1.0), NewRatio: 1.2, ChangedAt: base.Add(50 * time.Minute)},
		{SiteID: 1, AccountID: 2, ScanRunID: "r1", StableGroupKey: "id:1", ChangeType: "ratio_changed", ModelName: "g", OldRatio: ptrFloat(1.0), NewRatio: 1.2, ChangedAt: base.Add(51 * time.Minute)},
		// r1 批次：同分组但旧值不同 → 不得合并
		{SiteID: 1, AccountID: 1, ScanRunID: "r1", StableGroupKey: "id:3", ChangeType: "ratio_changed", ModelName: "g3", OldRatio: ptrFloat(1.0), NewRatio: 2.0, ChangedAt: base.Add(10 * time.Minute)},
		{SiteID: 1, AccountID: 2, ScanRunID: "r1", StableGroupKey: "id:3", ChangeType: "ratio_changed", ModelName: "g3", OldRatio: ptrFloat(0.5), NewRatio: 2.0, ChangedAt: base.Add(11 * time.Minute)},
		// r2 批次：与 r1 合并组数值完全相同 → 跨批次不得合并
		{SiteID: 1, AccountID: 1, ScanRunID: "r2", StableGroupKey: "id:1", ChangeType: "ratio_changed", ModelName: "g", OldRatio: ptrFloat(1.0), NewRatio: 1.2, ChangedAt: base.Add(30 * time.Minute)},
		// 空 scan_run_id：两行数值相同 → 各自独立成组
		{SiteID: 1, AccountID: 1, ScanRunID: "", StableGroupKey: "id:9", ChangeType: "added", ModelName: "g9", NewRatio: 3, ChangedAt: base.Add(20 * time.Minute)},
		{SiteID: 1, AccountID: 2, ScanRunID: "", StableGroupKey: "id:9", ChangeType: "added", ModelName: "g9", NewRatio: 3, ChangedAt: base.Add(21 * time.Minute)},
		// 另一站点独立批次
		{SiteID: 2, AccountID: 3, ScanRunID: "r3", StableGroupKey: "id:1", ChangeType: "ratio_changed", ModelName: "g", OldRatio: ptrFloat(1.0), NewRatio: 1.2, ChangedAt: base.Add(40 * time.Minute)},
	}
	for i := range rows {
		row := rows[i]
		if err := rates.AppendChange(&row); err != nil {
			t.Fatalf("append change %d: %v", i, err)
		}
	}
	return base
}

func TestListChangeGroupsPageMergesSameBatchChange(t *testing.T) {
	db := newSiteAccountTestDB(t)
	rates := NewRates(db)
	base := seedRateChangeRows(t, rates)

	groups, total, err := rates.ListChangeGroupsPage(0, 1, 50)
	if err != nil {
		t.Fatalf("list change groups: %v", err)
	}
	if total != 7 || len(groups) != 7 {
		t.Fatalf("total = %d, groups = %d, want 7", total, len(groups))
	}

	merged := groups[0]
	if len(merged.Members) != 2 || merged.Members[0].AccountID != 1 || merged.Members[1].AccountID != 2 {
		t.Fatalf("merged members = %#v", merged.Members)
	}
	if merged.Latest.ScanRunID != "r1" || merged.Latest.ModelName != "g" {
		t.Fatalf("merged latest = %#v", merged.Latest)
	}
	if merged.ChangedAt.Unix() != base.Add(51*time.Minute).Unix() {
		t.Fatalf("merged changed_at = %v, want group max", merged.ChangedAt)
	}

	// 跨批次数值相同不合并：r2 独立单成员
	crossBatch := groups[2]
	if crossBatch.Latest.ScanRunID != "r2" || len(crossBatch.Members) != 1 {
		t.Fatalf("cross-batch group = %#v", crossBatch)
	}

	// 空 scan_run_id 各自成组
	if groups[3].Latest.ScanRunID != "" || groups[4].Latest.ScanRunID != "" ||
		len(groups[3].Members) != 1 || len(groups[4].Members) != 1 {
		t.Fatalf("empty-run groups = %#v / %#v", groups[3], groups[4])
	}

	// 同批次旧值不同不合并
	if groups[5].Latest.StableGroupKey != "id:3" || groups[6].Latest.StableGroupKey != "id:3" ||
		len(groups[5].Members) != 1 || len(groups[6].Members) != 1 {
		t.Fatalf("different-value groups = %#v / %#v", groups[5], groups[6])
	}
}

func TestListChangeGroupsPageDoesNotSplitGroupAcrossPages(t *testing.T) {
	db := newSiteAccountTestDB(t)
	rates := NewRates(db)
	seedRateChangeRows(t, rates)

	groups, total, err := rates.ListChangeGroupsPage(0, 1, 1)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if total != 7 {
		t.Fatalf("total = %d, want group-count semantics", total)
	}
	if len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("first page must contain the whole merged group: %#v", groups)
	}

	memberSum := 0
	for page := 1; page <= 7; page++ {
		pageGroups, _, err := rates.ListChangeGroupsPage(0, page, 1)
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		if len(pageGroups) != 1 {
			t.Fatalf("page %d groups = %d, want 1", page, len(pageGroups))
		}
		memberSum += len(pageGroups[0].Members)
	}
	if memberSum != 8 {
		t.Fatalf("member sum across pages = %d, want all 8 rows exactly once", memberSum)
	}
}

func TestListChangeGroupsPageAccountFilterIsIdentity(t *testing.T) {
	db := newSiteAccountTestDB(t)
	rates := NewRates(db)
	seedRateChangeRows(t, rates)

	groups, total, err := rates.ListChangeGroupsPage(1, 1, 50)
	if err != nil {
		t.Fatalf("list filtered groups: %v", err)
	}
	if total != 4 || len(groups) != 4 {
		t.Fatalf("filtered total = %d, groups = %d, want 4 single-member groups", total, len(groups))
	}
	for _, group := range groups {
		if len(group.Members) != 1 || group.Members[0].AccountID != 1 {
			t.Fatalf("filtered group must contain only account 1: %#v", group)
		}
	}
}
