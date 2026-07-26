package notify

import (
	"testing"

	"github.com/bejix/upstream-ops/backend/storage"
)

func TestSubscriptionMatchesAccountAllEvents(t *testing.T) {
	sub := Subscription{
		AccountIDs: []uint{1},
		Mode:       SubscriptionModeGroups,
		Groups:     []string{"beta"},
	}

	if !sub.Matches(Message{AccountID: 1, Event: storage.EventAnnouncement}) {
		t.Fatal("account subscription should match non-rate events")
	}
	if !sub.Matches(Message{AccountID: 1, Event: storage.EventRateChanged, ModelName: "beta"}) {
		t.Fatal("account subscription should match selected rate group")
	}
	if sub.Matches(Message{AccountID: 1, Event: storage.EventRateChanged, ModelName: "gamma"}) {
		t.Fatal("account subscription should reject unselected rate group")
	}
}

func TestSubscriptionMatchesSpecifiedEvents(t *testing.T) {
	sub := Subscription{
		AccountIDs: []uint{1},
		Mode:       SubscriptionModeAll,
		Events: []storage.NotificationEvent{
			storage.EventAnnouncement,
			storage.EventBalanceLow,
		},
	}

	if !sub.Matches(Message{AccountID: 1, Event: storage.EventAnnouncement}) {
		t.Fatal("subscription should match selected announcement event")
	}
	if !sub.Matches(Message{AccountID: 1, Event: storage.EventBalanceLow}) {
		t.Fatal("subscription should match selected balance event")
	}
	if sub.Matches(Message{AccountID: 1, Event: storage.EventMonitorFailed}) {
		t.Fatal("subscription should reject unselected event")
	}
	if sub.Matches(Message{AccountID: 2, Event: storage.EventAnnouncement}) {
		t.Fatal("subscription should reject another account")
	}
}

func TestSubscriptionMatchesSpecifiedEventsAndGroups(t *testing.T) {
	sub := Subscription{
		AccountIDs: []uint{1},
		Mode:       SubscriptionModeGroups,
		Groups:     []string{"beta"},
		Events: []storage.NotificationEvent{
			storage.EventRateChanged,
			storage.EventSubscriptionExpiring,
		},
	}

	if !sub.Matches(Message{AccountID: 1, Event: storage.EventRateChanged, ModelName: "beta"}) {
		t.Fatal("subscription should match selected rate event and group")
	}
	if sub.Matches(Message{AccountID: 1, Event: storage.EventRateChanged, ModelName: "gamma"}) {
		t.Fatal("subscription should reject selected rate event with unselected group")
	}
	if !sub.Matches(Message{AccountID: 1, Event: storage.EventSubscriptionExpiring}) {
		t.Fatal("subscription should match selected non-rate event without group")
	}
	if sub.Matches(Message{AccountID: 1, Event: storage.EventAnnouncement}) {
		t.Fatal("subscription should reject unselected non-rate event")
	}
}

// 多选账号：一条规则覆盖多个上游账号，任一命中即放行。
func TestSubscriptionMatchesMultipleAccounts(t *testing.T) {
	sub := Subscription{
		AccountIDs: []uint{1, 2, 3},
		Mode:       SubscriptionModeAll,
	}

	if !sub.Matches(Message{AccountID: 1, Event: storage.EventAnnouncement}) {
		t.Fatal("subscription should match first account")
	}
	if !sub.Matches(Message{AccountID: 2, Event: storage.EventBalanceLow}) {
		t.Fatal("subscription should match second account")
	}
	if !sub.Matches(Message{AccountID: 3, Event: storage.EventMonitorFailed}) {
		t.Fatal("subscription should match third account")
	}
	if sub.Matches(Message{AccountID: 4, Event: storage.EventAnnouncement}) {
		t.Fatal("subscription should reject account not in list")
	}
}

func TestParseSubscriptionsRejectsLegacyChannelID(t *testing.T) {
	list, err := ParseSubscriptions(`[{"channel_id":7,"mode":"all"}]`)
	if err == nil || list != nil {
		t.Fatalf("legacy channel_id must be rejected, list=%+v err=%v", list, err)
	}
}

func TestParseSubscriptionsAcceptsAccountScope(t *testing.T) {
	list, err := ParseSubscriptions(`[{"account_ids":[7,8],"mode":"all"}]`)
	if err != nil || len(list) != 1 || len(list[0].AccountIDs) != 2 {
		t.Fatalf("parse account subscription list=%+v err=%v", list, err)
	}
}

func TestSubscriptionMatchesSiteAndAccountScopes(t *testing.T) {
	siteSubscription := Subscription{SiteIDs: []uint{3}, Mode: SubscriptionModeAll}
	if !siteSubscription.Matches(Message{SiteID: 3, AccountIDs: []uint{7, 8}, Event: storage.EventRateChanged}) {
		t.Fatal("site subscription should match aggregated site event")
	}
	accountSubscription := Subscription{AccountIDs: []uint{7}, Mode: SubscriptionModeAll}
	if !accountSubscription.Matches(Message{SiteID: 3, AccountIDs: []uint{7, 8}, Event: storage.EventRateChanged}) {
		t.Fatal("account subscription should match aggregate containing selected account")
	}
	if accountSubscription.Matches(Message{SiteID: 3, AccountIDs: []uint{8}, Event: storage.EventRateChanged}) {
		t.Fatal("account subscription should reject aggregate without selected account")
	}
	overlap := Subscription{SiteIDs: []uint{3}, AccountIDs: []uint{7}, Mode: SubscriptionModeAll}
	if !overlap.Matches(Message{SiteID: 3, AccountIDs: []uint{7}, Event: storage.EventRateChanged}) {
		t.Fatal("overlapping scope should still match once")
	}
}
