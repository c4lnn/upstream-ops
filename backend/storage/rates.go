package storage

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Rates struct{ db *gorm.DB }

func NewRates(db *gorm.DB) *Rates { return &Rates{db: db} }

var trendNow = time.Now
var trendLocation = loadTrendLocation()

func loadTrendLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return loc
}

// ListByAccount returns all current rate snapshots for an account.
func (r *Rates) ListByAccount(accountID uint) ([]RateSnapshot, error) {
	var list []RateSnapshot
	if err := r.db.Where("account_id = ?", accountID).Order("model_name ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// Upsert 更新或插入倍率快照，返回此前的记录（若有），调用方据此判断是否变化。
func (r *Rates) Upsert(snapshot *RateSnapshot) (*RateSnapshot, error) {
	if snapshot.StableGroupKey == "" {
		snapshot.StableGroupKey = StableRateGroupKey(snapshot.RemoteGroupID, snapshot.ModelName)
	}
	var prev RateSnapshot
	err := r.db.
		Where("account_id = ? AND stable_group_key = ?", snapshot.AccountID, snapshot.StableGroupKey).
		First(&prev).Error
	switch {
	case err == nil:
		old := prev
		prev.Ratio = snapshot.Ratio
		prev.CompletionRatio = snapshot.CompletionRatio
		prev.RemoteGroupID = snapshot.RemoteGroupID
		prev.StableGroupKey = snapshot.StableGroupKey
		prev.ModelName = snapshot.ModelName
		prev.Description = snapshot.Description
		prev.LastSeenAt = snapshot.LastSeenAt
		if err := r.db.Save(&prev).Error; err != nil {
			return nil, err
		}
		return &old, nil
	case err == gorm.ErrRecordNotFound:
		snapshot.FirstSeenAt = snapshot.LastSeenAt
		if err := r.db.Create(snapshot).Error; err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, err
	}
}

func (r *Rates) AppendChange(log *RateChangeLog) error {
	if log.ChangedAt.IsZero() {
		log.ChangedAt = time.Now()
	}
	return r.db.Create(log).Error
}

func (r *Rates) DeleteSnapshot(accountID uint, modelName string) error {
	return r.db.Where("account_id = ? AND model_name = ?", accountID, modelName).Delete(&RateSnapshot{}).Error
}

func (r *Rates) DeleteSnapshotByKey(accountID uint, stableGroupKey string) error {
	return r.db.Where("account_id = ? AND stable_group_key = ?", accountID, stableGroupKey).Delete(&RateSnapshot{}).Error
}

func StableRateGroupKey(remoteGroupID *int64, modelName string) string {
	if remoteGroupID != nil {
		return fmt.Sprintf("id:%d", *remoteGroupID)
	}
	return "name:" + strings.TrimSpace(modelName)
}

// RateChangeGroup 一次扫描批次内的同一变化及其全部账号成员行。
// Latest 是组内 id 最大的代表行；ChangedAt 取组内最大变化时间。
type RateChangeGroup struct {
	Latest    RateChangeLog
	ChangedAt time.Time
	Members   []RateChangeLog
}

// rateChangeGroupSQLKey 与 rateChangeGroupKey 必须表达同一合并语义：
// site_id + scan_run_id + 分组 + 变化类型 + 新旧倍率；空 scan_run_id 的行按自身 id 独立成组。
const rateChangeGroupSQLKey = "site_id, scan_run_id, stable_group_key, change_type, old_ratio, new_ratio, old_completion_ratio, new_completion_ratio, CASE WHEN scan_run_id = '' THEN id ELSE 0 END"

func rateChangeGroupKey(l *RateChangeLog) string {
	if l.ScanRunID == "" {
		return fmt.Sprintf("row|%d", l.ID)
	}
	return fmt.Sprintf("%d|%s|%s|%s|%s|%g|%s|%g",
		l.SiteID, l.ScanRunID, l.StableGroupKey, l.ChangeType,
		floatPtrKey(l.OldRatio), l.NewRatio, floatPtrKey(l.OldCompletionRatio), l.NewCompletionRatio)
}

func floatPtrKey(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%g", *v)
}

func (r *Rates) changeGroupQuery(accountID uint) *gorm.DB {
	q := r.db.Model(&RateChangeLog{}).Group(rateChangeGroupSQLKey)
	if accountID != 0 {
		q = q.Where("account_id = ?", accountID)
	}
	return q
}

// ListChangeGroupsPage 按扫描批次聚合分页倍率变化：
// total、page、pageSize 均以聚合组为单位，组按最大 changed_at 倒序，合并组不会被分页劈开。
// accountID != 0 时先过滤成员再聚合，单账号视角下聚合为恒等操作。
func (r *Rates) ListChangeGroupsPage(accountID uint, page, pageSize int) ([]RateChangeGroup, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	var total int64
	countQuery := r.changeGroupQuery(accountID).Select("1")
	if err := r.db.Table("(?) AS grouped_changes", countQuery).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []RateChangeGroup{}, 0, nil
	}

	var reps []struct {
		RepID uint `gorm:"column:rep_id"`
	}
	if err := r.changeGroupQuery(accountID).
		Select("MAX(id) AS rep_id").
		Order("MAX(changed_at) DESC, MAX(id) DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&reps).Error; err != nil {
		return nil, 0, err
	}
	if len(reps) == 0 {
		return []RateChangeGroup{}, total, nil
	}

	repIDs := make([]uint, 0, len(reps))
	repOrder := make(map[uint]int, len(reps))
	for i, rep := range reps {
		repIDs = append(repIDs, rep.RepID)
		repOrder[rep.RepID] = i
	}
	var repRows []RateChangeLog
	if err := r.db.Where("id IN ?", repIDs).Find(&repRows).Error; err != nil {
		return nil, 0, err
	}

	groups := make([]RateChangeGroup, len(reps))
	position := make(map[string]int, len(repRows))
	runIDs := make([]string, 0, len(repRows))
	seenRuns := make(map[string]struct{}, len(repRows))
	emptyRunIDs := make([]uint, 0)
	for i := range repRows {
		row := repRows[i]
		pos := repOrder[row.ID]
		groups[pos] = RateChangeGroup{Latest: row}
		position[rateChangeGroupKey(&row)] = pos
		if row.ScanRunID == "" {
			emptyRunIDs = append(emptyRunIDs, row.ID)
			continue
		}
		if _, ok := seenRuns[row.ScanRunID]; !ok {
			seenRuns[row.ScanRunID] = struct{}{}
			runIDs = append(runIDs, row.ScanRunID)
		}
	}

	memberQuery := r.db.Model(&RateChangeLog{})
	if accountID != 0 {
		memberQuery = memberQuery.Where("account_id = ?", accountID)
	}
	switch {
	case len(runIDs) > 0 && len(emptyRunIDs) > 0:
		memberQuery = memberQuery.Where("scan_run_id IN ? OR id IN ?", runIDs, emptyRunIDs)
	case len(runIDs) > 0:
		memberQuery = memberQuery.Where("scan_run_id IN ?", runIDs)
	default:
		memberQuery = memberQuery.Where("id IN ?", emptyRunIDs)
	}
	var members []RateChangeLog
	if err := memberQuery.Order("account_id ASC, id ASC").Find(&members).Error; err != nil {
		return nil, 0, err
	}

	for i := range members {
		row := members[i]
		pos, ok := position[rateChangeGroupKey(&row)]
		if !ok {
			continue
		}
		groups[pos].Members = append(groups[pos].Members, row)
		if row.ChangedAt.After(groups[pos].ChangedAt) {
			groups[pos].ChangedAt = row.ChangedAt
		}
	}
	return groups, total, nil
}

func (r *Rates) AppendBalance(s *BalanceSnapshot) error {
	if s.SampledAt.IsZero() {
		s.SampledAt = time.Now()
	}
	return r.db.Create(s).Error
}

func (r *Rates) AppendCost(s *CostSnapshot) error {
	if s.SampledAt.IsZero() {
		s.SampledAt = time.Now()
	}
	return r.db.Create(s).Error
}

// DeleteBalanceSnapshotsBefore 删除 sampled_at < cutoff 的余额快照，返回删除行数。
func (r *Rates) DeleteBalanceSnapshotsBefore(cutoff time.Time) (int64, error) {
	res := r.db.Where("sampled_at < ?", cutoff).Delete(&BalanceSnapshot{})
	return res.RowsAffected, res.Error
}

// DeleteCostSnapshotsBefore 删除 sampled_at < cutoff 的消费快照，返回删除行数。
func (r *Rates) DeleteCostSnapshotsBefore(cutoff time.Time) (int64, error) {
	res := r.db.Where("sampled_at < ?", cutoff).Delete(&CostSnapshot{})
	return res.RowsAffected, res.Error
}

// BalanceHistory 倒序拉取余额历史。
func (r *Rates) BalanceHistory(accountID uint, limit int) ([]BalanceSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	var list []BalanceSnapshot
	if err := r.db.
		Where("account_id = ?", accountID).
		Order("sampled_at DESC").
		Limit(limit).
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DailyAggregate 一天的聚合余额（所有渠道之和）。
type DailyAggregate struct {
	Day     time.Time `json:"day"`
	Balance float64   `json:"balance"`
}

// DailyCostAggregate 一天的聚合消费（所有渠道之和）。
type DailyCostAggregate struct {
	Day  time.Time `json:"day"`
	Cost float64   `json:"cost"`
}

// AggregateBalanceTrend 取最近 N 天的"日内最后一次余额"按渠道之和，作为总余额趋势。
//
// 实现：对每个 (account_id, day) 取该天最后一次 BalanceSnapshot 的余额，再按 day 求和，
// 然后补齐窗口内缺失的日期。窗口内完全没有采样时返回空数组。
func (r *Rates) AggregateBalanceTrend(days int) ([]DailyAggregate, error) {
	if days <= 0 {
		days = 7
	}
	today := dayStart(trendNow())
	since := today.AddDate(0, 0, -(days - 1))

	var snapshots []BalanceSnapshot
	if err := r.db.
		Where("sampled_at >= ?", since).
		Order("sampled_at ASC").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return []DailyAggregate{}, nil
	}

	type key struct {
		AccountID uint
		Day       time.Time
	}

	latest := make(map[key]BalanceSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		day := dayStart(snapshot.SampledAt)
		latest[key{AccountID: snapshot.AccountID, Day: day}] = snapshot
	}

	byDay := make(map[string]float64, days)
	for _, snapshot := range latest {
		day := dayStart(snapshot.SampledAt)
		byDay[dayKey(day)] += snapshot.Balance
	}

	out := make([]DailyAggregate, 0, days)
	for day := since; !day.After(today); day = day.AddDate(0, 0, 1) {
		out = append(out, DailyAggregate{Day: day, Balance: byDay[dayKey(day)]})
	}
	return out, nil
}

// AggregateCostTrend 取最近 N 天的"日内最后一次今日消费"按渠道之和，作为总消费趋势。
func (r *Rates) AggregateCostTrend(days int) ([]DailyCostAggregate, error) {
	if days <= 0 {
		days = 7
	}
	today := dayStart(trendNow())
	since := today.AddDate(0, 0, -(days - 1))

	var snapshots []CostSnapshot
	if err := r.db.
		Where("sampled_at >= ?", since).
		Order("sampled_at ASC").
		Find(&snapshots).Error; err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return []DailyCostAggregate{}, nil
	}

	type key struct {
		AccountID uint
		Day       time.Time
	}

	latest := make(map[key]CostSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		day := dayStart(snapshot.SampledAt)
		latest[key{AccountID: snapshot.AccountID, Day: day}] = snapshot
	}

	byDay := make(map[string]float64, days)
	for _, snapshot := range latest {
		day := dayStart(snapshot.SampledAt)
		byDay[dayKey(day)] += snapshot.TodayCost
	}

	out := make([]DailyCostAggregate, 0, days)
	for day := since; !day.After(today); day = day.AddDate(0, 0, 1) {
		out = append(out, DailyCostAggregate{Day: day, Cost: byDay[dayKey(day)]})
	}
	return out, nil
}

func dayStart(t time.Time) time.Time {
	local := t.In(trendLocation)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, trendLocation)
}

func dayKey(t time.Time) string {
	return t.Format("2006-01-02")
}
