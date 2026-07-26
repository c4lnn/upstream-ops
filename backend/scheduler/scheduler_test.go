package scheduler

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/monitor"
	"github.com/bejix/upstream-ops/backend/storage"
	"gorm.io/gorm"
)

type fakeUpstreamSync struct {
	called atomic.Int32
}

func (f *fakeUpstreamSync) SyncAllOnRateScan(ctx context.Context) {
	f.called.Add(1)
}

type fakeMonitor struct {
	scanBalances func(context.Context)
	scanRates    func(context.Context)
}

func (m *fakeMonitor) ScanAllBalances(ctx context.Context) {
	if m.scanBalances != nil {
		m.scanBalances(ctx)
	}
}

func (m *fakeMonitor) ScanAllRates(ctx context.Context) {
	if m.scanRates != nil {
		m.scanRates(ctx)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := storage.Open(storage.DBConfig{
		Driver: storage.DBDriverSQLite,
		Path:   filepath.Join(t.TempDir(), "scheduler-test.db"),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := storage.AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestRunRetentionDeletesAnnouncements(t *testing.T) {
	db := openTestDB(t)
	announcements := storage.NewUpstreamAnnouncements(db)
	notifies := storage.NewNotifications(db)
	monLogs := storage.NewMonitorLogs(db)
	rates := storage.NewRates(db)
	syncLogs := storage.NewUpstreamSyncLogs(db)

	oldTime := time.Now().AddDate(0, 0, -10)
	if err := db.Create(&storage.UpstreamAnnouncement{
		SiteID:      1,
		AccountID:   1,
		SourceKey:   "old",
		Content:     "old",
		FirstSeenAt: oldTime,
	}).Error; err != nil {
		t.Fatalf("create announcement: %v", err)
	}

	s := New(
		config.SchedulerConfig{
			Retention: config.RetentionConfig{
				AnnouncementsDays: 1,
			},
		},
		&monitor.Service{},
		monLogs,
		syncLogs,
		rates,
		notifies,
		announcements,
		nil,
		nil,
		nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRetention()

	list, total, err := announcements.ListPage(1, 10)
	if err != nil {
		t.Fatalf("list announcements: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Fatalf("announcements not cleaned: total=%d list=%#v", total, list)
	}
}

func TestRunRetentionDeletesUpstreamSyncLogsWithMonitorLogDays(t *testing.T) {
	db := openTestDB(t)
	monLogs := storage.NewMonitorLogs(db)
	syncLogs := storage.NewUpstreamSyncLogs(db)
	rates := storage.NewRates(db)
	notifies := storage.NewNotifications(db)

	if err := syncLogs.Append(&storage.UpstreamSyncLog{
		SyncGroupID: 1,
		TargetID:    1,
		Action:      "apply",
		Success:     true,
		Message:     "old",
		CreatedAt:   time.Now().AddDate(0, 0, -10),
	}); err != nil {
		t.Fatalf("append old sync log: %v", err)
	}
	if err := syncLogs.Append(&storage.UpstreamSyncLog{
		SyncGroupID: 1,
		TargetID:    1,
		Action:      "apply",
		Success:     true,
		Message:     "new",
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("append new sync log: %v", err)
	}

	s := New(
		config.SchedulerConfig{
			Retention: config.RetentionConfig{
				MonitorLogsDays: 1,
			},
		},
		&monitor.Service{},
		monLogs,
		syncLogs,
		rates,
		notifies,
		nil,
		nil,
		nil,
		nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRetention()

	list, total, err := syncLogs.ListPage(1, 10)
	if err != nil {
		t.Fatalf("list sync logs: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Message != "new" {
		t.Fatalf("sync logs not cleaned: total=%d list=%#v", total, list)
	}
}

func TestRunRatesTriggersUpstreamSync(t *testing.T) {
	syncSvc := &fakeUpstreamSync{}
	s := New(
		config.SchedulerConfig{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		syncSvc,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRates()

	if got := syncSvc.called.Load(); got != 1 {
		t.Fatalf("sync calls = %d, want 1", got)
	}
}

func TestRunBalanceKeepsLeaseUntilTimedOutScanReturns(t *testing.T) {
	var logs bytes.Buffer
	started := make(chan struct{})
	timedOut := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	monitorSvc := &fakeMonitor{
		scanBalances: func(ctx context.Context) {
			if calls.Add(1) != 1 {
				return
			}
			close(started)
			<-ctx.Done()
			close(timedOut)
			<-release
		},
	}
	s := New(
		config.SchedulerConfig{BalanceTimeoutSeconds: 1},
		monitorSvc,
		nil, nil, nil, nil, nil, nil, nil, nil,
		config.ProxyConfig{},
		slog.New(slog.NewTextHandler(&logs, nil)),
	)

	finished := make(chan struct{})
	go func() {
		s.runBalance()
		close(finished)
	}()
	waitForSignal(t, started, "balance scan start")
	waitForSignal(t, timedOut, "balance scan timeout")

	// The timeout has cancelled the scan context, but the callback has not
	// returned. A second tick must still skip rather than overlap it.
	s.runBalance()
	if got := calls.Load(); got != 1 {
		t.Fatalf("balance calls while first task is still returning = %d, want 1", got)
	}
	select {
	case <-finished:
		t.Fatal("timed-out scan released its task lease before returning")
	default:
	}

	close(release)
	waitForSignal(t, finished, "first balance scan finish")
	s.runBalance()
	if got := calls.Load(); got != 2 {
		t.Fatalf("balance calls after release = %d, want 2", got)
	}

	output := logs.String()
	if !strings.Contains(output, "task=balance") || !strings.Contains(output, "outcome=skipped") {
		t.Fatalf("missing structured skip log: %s", output)
	}
	if !strings.Contains(output, "outcome=timed_out") {
		t.Fatalf("missing timeout log: %s", output)
	}
}

func TestSharedCoordinatorSeparatesTaskTypesAcrossSchedulers(t *testing.T) {
	shared := NewTaskRunCoordinator()
	balanceStarted := make(chan struct{})
	releaseBalance := make(chan struct{})
	balanceFinished := make(chan struct{})
	var balanceCalls atomic.Int32
	var rateCalls atomic.Int32
	monitorSvc := &fakeMonitor{
		scanBalances: func(context.Context) {
			if balanceCalls.Add(1) == 1 {
				close(balanceStarted)
				<-releaseBalance
			}
		},
		scanRates: func(context.Context) {
			rateCalls.Add(1)
		},
	}
	first := New(
		config.SchedulerConfig{BalanceTimeoutSeconds: 30}, monitorSvc,
		nil, nil, nil, nil, nil, nil, nil, nil,
		config.ProxyConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), shared,
	)
	second := New(
		config.SchedulerConfig{BalanceTimeoutSeconds: 30, RateTimeoutSeconds: 30}, monitorSvc,
		nil, nil, nil, nil, nil, nil, nil, nil,
		config.ProxyConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), shared,
	)

	go func() {
		first.runBalance()
		close(balanceFinished)
	}()
	waitForSignal(t, balanceStarted, "first scheduler balance scan start")

	second.runRates()
	if got := rateCalls.Load(); got != 1 {
		t.Fatalf("rate task was blocked by balance task: calls=%d", got)
	}
	second.runBalance()
	if got := balanceCalls.Load(); got != 1 {
		t.Fatalf("overlapping balance task was allowed: calls=%d", got)
	}

	close(releaseBalance)
	waitForSignal(t, balanceFinished, "first scheduler balance scan finish")
}

func TestRunRatesSkipsUpstreamSyncAfterTimeout(t *testing.T) {
	timedOut := make(chan struct{})
	monitorSvc := &fakeMonitor{
		scanRates: func(ctx context.Context) {
			<-ctx.Done()
			close(timedOut)
		},
	}
	syncSvc := &fakeUpstreamSync{}
	s := New(
		config.SchedulerConfig{RateTimeoutSeconds: 1}, monitorSvc,
		nil, nil, nil, nil, nil, nil, nil, syncSvc,
		config.ProxyConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	s.runRates()
	waitForSignal(t, timedOut, "rate scan timeout")
	if got := syncSvc.called.Load(); got != 0 {
		t.Fatalf("upstream sync calls after rate timeout = %d, want 0", got)
	}
}

func TestSchedulerTaskTimeoutDefaultsToFiveMinutes(t *testing.T) {
	if got := schedulerTaskTimeout(0); got != 5*time.Minute {
		t.Fatalf("default task timeout = %s, want 5m", got)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
