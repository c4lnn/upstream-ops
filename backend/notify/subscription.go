package notify

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/bejix/upstream-ops/backend/storage"
)

// SubscriptionMode 倍率分组过滤维度。
//
//   - all    订阅该上游命中事件的所有分组倍率
//   - groups 仅订阅该上游 + 指定分组（model_name）的倍率相关事件；
//     非倍率事件仍命中（分组过滤仅对倍率事件起作用）
type SubscriptionMode string

const (
	SubscriptionModeAll    SubscriptionMode = "all"
	SubscriptionModeGroups SubscriptionMode = "groups"
)

// Subscription 通知渠道对一组站点或账号的订阅规则。
type Subscription struct {
	AccountIDs []uint                      `json:"account_ids,omitempty"`
	SiteIDs    []uint                      `json:"site_ids,omitempty"`
	Mode       SubscriptionMode            `json:"mode"`
	Groups     []string                    `json:"groups,omitempty"`
	Events     []storage.NotificationEvent `json:"events,omitempty"`
}

// ParseSubscriptions parses only the final site/account subscription schema.
// Invalid persisted data is reported to the caller instead of becoming an
// accidental "subscribe to everything" rule.
func ParseSubscriptions(raw string) ([]Subscription, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil, nil
	}
	var list []Subscription
	decoder := json.NewDecoder(bytes.NewReader([]byte(s)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// Matches 判断这条订阅是否覆盖当前消息：
//   - 上游账号或站点范围必须命中
//   - Events 为空表示全部事件；非空时消息事件必须在 Events 中
//   - 倍率相关事件 + mode=groups 时，model_name 必须在 Groups 中
//   - 其它情况只要上游匹配即放行
func (s Subscription) Matches(msg Message) bool {
	if msg.AccountID == 0 && msg.SiteID == 0 && len(msg.AccountIDs) == 0 {
		return s.matchesEvent(msg.Event)
	}
	if !s.matchesScope(msg) {
		return false
	}
	if !s.matchesEvent(msg.Event) {
		return false
	}
	if !isRateEvent(msg.Event) || s.Mode != SubscriptionModeGroups {
		return true
	}
	for _, g := range s.Groups {
		if g == msg.ModelName {
			return true
		}
	}
	return false
}

func (s Subscription) matchesScope(msg Message) bool {
	if msg.SiteID != 0 {
		for _, id := range s.SiteIDs {
			if id == msg.SiteID {
				return true
			}
		}
	}
	ids := append([]uint{}, msg.AccountIDs...)
	if msg.AccountID != 0 {
		ids = append(ids, msg.AccountID)
	}
	for _, wanted := range s.AccountIDs {
		for _, actual := range ids {
			if wanted == actual {
				return true
			}
		}
	}
	return len(s.SiteIDs) == 0 && len(s.AccountIDs) == 0
}

func (s Subscription) matchesEvent(event storage.NotificationEvent) bool {
	if len(s.Events) == 0 {
		return true
	}
	for _, e := range s.Events {
		if e == event {
			return true
		}
	}
	return false
}

func isRateEvent(event storage.NotificationEvent) bool {
	return event == storage.EventRateChanged ||
		event == storage.EventRateStructureChanged ||
		event == storage.EventRateAdded ||
		event == storage.EventRateRemoved
}

// AnyMatch 任意一条订阅命中即视为该通知渠道关心此消息。
// 调用方应在 len(subs) > 0 时才调；空切片由调用方按"订阅一切"处理。
func AnyMatch(subs []Subscription, msg Message) bool {
	for i := range subs {
		if subs[i].Matches(msg) {
			return true
		}
	}
	return false
}
