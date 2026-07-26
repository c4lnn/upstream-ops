package notify

import (
	"math"
	"time"

	"github.com/bejix/upstream-ops/backend/storage"
)

// Policy 通知去抖策略。所有字段都是面向"少烦用户"取向：
//   - BatchRateChanges：同次扫描中合并多条倍率相关通知
//   - MinChangePct：涨跌幅小于阈值时跳过推送（仍写入 RateChangeLog 表）
//   - BalanceLowCooldown：同渠道 balance_low 在窗口内不重复发送
//   - SendMaxAttempts：单条消息最多发送尝试次数（含首发），<=1 表示不重试
type Policy struct {
	NotificationPrefix                       string
	BatchRateChanges                         bool
	MinChangePct                             float64
	BalanceLowCooldown                       time.Duration
	SubscriptionDailyRemainingThresholdPct   float64
	SubscriptionWeeklyRemainingThresholdPct  float64
	SubscriptionMonthlyRemainingThresholdPct float64
	SubscriptionExpiryThreshold              time.Duration
	SubscriptionAlertCooldown                time.Duration
	SendMaxAttempts                          int
}

// CooldownStore Dispatcher 用来判断某个 (accountID, event) 是否还在冷却窗口。
//
// 抽象成 interface 是为了让 dispatcher 不依赖具体存储；
// 生产实现是 *storage.Notifications.TryClaimCooldown；
// 测试时可以注入一个内存 stub。
type CooldownStore interface {
	TryClaimCooldown(accountID uint, event storage.NotificationEvent, cooldown time.Duration) (bool, error)
}

// RateChange 是一条待发送的倍率相关记录（去抖 / 合并的基本单元）。
type RateChange struct {
	GroupName string
	OldRatio  float64
	NewRatio  float64
	OldComp   float64
	NewComp   float64
	ChangedAt time.Time
}

// SiteRateChange 是站点批次中某个账号的一条倍率/分组变化。
// 数据按账号产生，Dispatcher 负责在站点和批次范围内聚合账号列表。
type SiteRateChange struct {
	SiteID      uint
	AccountID   uint
	AccountName string
	ScanRunID   string
	StableKey   string
	ChangeType  string
	RateChange
}

// ChangePctAbove 涨跌幅是否达到阈值。
// minPct = 0 表示不过滤。OldRatio = 0 时按"新出现的分组"处理，永远算"达到阈值"。
func (rc RateChange) ChangePctAbove(minPct float64) bool {
	if minPct <= 0 {
		return true
	}
	if rc.OldRatio == 0 {
		return true
	}
	pct := math.Abs(rc.NewRatio-rc.OldRatio) / math.Abs(rc.OldRatio) * 100
	return pct >= minPct
}

func arrowFor(oldV, newV float64) string {
	switch {
	case newV > oldV:
		return "上涨"
	case newV < oldV:
		return "下调"
	default:
		return "调整"
	}
}
