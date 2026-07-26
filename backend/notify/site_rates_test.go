package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
)

func TestDispatchSiteRateBatchFansOutBeforeDeduplication(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "notify.db"),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&storage.NotificationChannel{},
		&storage.NotificationLog{},
		&storage.NotificationCooldown{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	cipher, err := crypto.NewCipher("test-secret")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	configCipher, err := cipher.Encrypt(`{"url":"` + server.URL + `"}`)
	if err != nil {
		t.Fatalf("encrypt config: %v", err)
	}
	repo := storage.NewNotifications(db)
	for _, name := range []string{"webhook-a", "webhook-b"} {
		if err := repo.CreateChannel(&storage.NotificationChannel{
			Name:         name,
			Type:         storage.NotifyWebhook,
			ConfigCipher: configCipher,
			Enabled:      true,
		}); err != nil {
			t.Fatalf("create notification channel %s: %v", name, err)
		}
	}

	dispatcher := NewDispatcher(repo, cipher, nil, Policy{SendMaxAttempts: 1})
	changes := []SiteRateChange{{
		SiteID:      7,
		AccountID:   11,
		AccountName: "主账号",
		ScanRunID:   "scan-1",
		StableKey:   "default",
		ChangeType:  "ratio_changed",
		RateChange:  RateChange{GroupName: "default", OldRatio: 1, NewRatio: 1.2},
	}}

	if err := dispatcher.DispatchSiteRateBatch(context.Background(), storage.UpstreamSite{ID: 7, Name: "测试站点"}, changes, nil); err != nil {
		t.Fatalf("dispatch site rate batch: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("webhook requests = %d, want 2", got)
	}

	if err := dispatcher.DispatchSiteRateBatch(context.Background(), storage.UpstreamSite{ID: 7, Name: "测试站点"}, changes, nil); err != nil {
		t.Fatalf("dispatch duplicate batch: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("duplicate batch requests = %d, want 2", got)
	}
}

func TestSiteRateAggregationMergesEqualChangesAndKeepsDifferences(t *testing.T) {
	base := SiteRateChange{
		SiteID: 7, AccountID: 11, AccountName: "账号 A", ScanRunID: "scan-1",
		StableKey: "id:1", ChangeType: "ratio_changed",
		RateChange: RateChange{GroupName: "default", OldRatio: 1, NewRatio: 1.2},
	}
	changes := []SiteRateChange{
		base,
		base,
		{
			SiteID: 7, AccountID: 12, AccountName: "账号 B", ScanRunID: "scan-1",
			StableKey: "id:1", ChangeType: "ratio_changed",
			RateChange: RateChange{GroupName: "default", OldRatio: 1, NewRatio: 1.2},
		},
		{
			SiteID: 7, AccountID: 13, AccountName: "账号 C", ScanRunID: "scan-1",
			StableKey: "id:1", ChangeType: "ratio_changed",
			RateChange: RateChange{GroupName: "default", OldRatio: 0.8, NewRatio: 1},
		},
	}
	deduped := dedupeSiteRateChanges(changes)
	if len(deduped) != 3 {
		t.Fatalf("deduped changes = %d, want 3", len(deduped))
	}
	groups := aggregateSiteChanges(deduped)
	if len(groups) != 2 {
		t.Fatalf("aggregated groups = %d, want 2", len(groups))
	}
	if got := strings.Join(groups[0].AccountNames, ","); got != "账号 A,账号 B" {
		t.Fatalf("merged account names = %q", got)
	}

	message := BuildSiteRateMessage(
		storage.UpstreamSite{ID: 7, Name: "测试站点"},
		storage.EventRateChanged,
		deduped,
		[]string{"账号 D：超时"},
	)
	if !message.Partial || len(message.AccountIDs) != 3 {
		t.Fatalf("message metadata = %#v", message)
	}
	for _, want := range []string{"账号 A、账号 B", "账号 C", "未完成扫描账号：账号 D：超时"} {
		if !strings.Contains(message.Body, want) {
			t.Fatalf("message body missing %q: %s", want, message.Body)
		}
	}
}

func TestFilterSiteChangesSupportsSiteAndAccountScopesWithoutDuplicates(t *testing.T) {
	changes := []SiteRateChange{
		{SiteID: 7, AccountID: 11, AccountName: "账号 A", ScanRunID: "scan-1", StableKey: "id:1", ChangeType: "ratio_changed", RateChange: RateChange{GroupName: "default"}},
		{SiteID: 7, AccountID: 12, AccountName: "账号 B", ScanRunID: "scan-1", StableKey: "id:1", ChangeType: "ratio_changed", RateChange: RateChange{GroupName: "default"}},
	}
	siteMatches := filterSiteChanges(7, storage.EventRateChanged, changes, []Subscription{{SiteIDs: []uint{7}}})
	if len(siteMatches) != 2 {
		t.Fatalf("site matches = %d, want 2", len(siteMatches))
	}
	accountMatches := filterSiteChanges(7, storage.EventRateChanged, changes, []Subscription{{AccountIDs: []uint{11}}})
	if len(accountMatches) != 1 || accountMatches[0].AccountID != 11 {
		t.Fatalf("account matches = %#v", accountMatches)
	}
	overlapMatches := filterSiteChanges(7, storage.EventRateChanged, changes, []Subscription{{SiteIDs: []uint{7}, AccountIDs: []uint{11}}})
	if len(overlapMatches) != 2 {
		t.Fatalf("overlap matches = %d, want 2", len(overlapMatches))
	}
}

func TestSiteRateEventKeyIsOrderStableAndScanScoped(t *testing.T) {
	first := SiteRateChange{SiteID: 7, AccountID: 11, ScanRunID: "scan-1", StableKey: "id:1", ChangeType: "ratio_changed", RateChange: RateChange{OldRatio: 1, NewRatio: 1.2}}
	second := SiteRateChange{SiteID: 7, AccountID: 12, ScanRunID: "scan-1", StableKey: "id:2", ChangeType: "removed", RateChange: RateChange{OldRatio: 2}}
	keyA := siteRateEventKey(7, storage.EventRateChanged, []SiteRateChange{first, second})
	keyB := siteRateEventKey(7, storage.EventRateChanged, []SiteRateChange{second, first})
	if keyA != keyB {
		t.Fatalf("event key depends on input order: %q != %q", keyA, keyB)
	}
	first.ScanRunID = "scan-2"
	second.ScanRunID = "scan-2"
	if keyA == siteRateEventKey(7, storage.EventRateChanged, []SiteRateChange{first, second}) {
		t.Fatal("event key unexpectedly deduplicates across scan runs")
	}
}
