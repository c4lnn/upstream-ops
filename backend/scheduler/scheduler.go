// Package scheduler 用 robfig/cron 触发周期性扫描。
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/bejix/upstream-ops/backend/captcha"
	"github.com/bejix/upstream-ops/backend/config"
	"github.com/bejix/upstream-ops/backend/crypto"
	"github.com/bejix/upstream-ops/backend/storage"
	"github.com/robfig/cron/v3"
)

// monitoringService is intentionally narrow so scheduler behavior can be
// tested without constructing a real monitor service or contacting upstreams.
type monitoringService interface {
	ScanAllBalances(context.Context)
	ScanAllRates(context.Context)
}

type Scheduler struct {
	cfg           config.SchedulerConfig
	log           *slog.Logger
	cron          *cron.Cron
	monitor       monitoringService
	monLogs       *storage.MonitorLogs
	syncLogs      *storage.UpstreamSyncLogs
	rates         *storage.Rates
	notifies      *storage.Notifications
	announcements *storage.UpstreamAnnouncements
	captchas      *storage.Captchas
	cipher        *crypto.Cipher
	upstreamSync  upstreamSyncService
	proxy         config.ProxyConfig
	coordinator   *TaskRunCoordinator
}

type upstreamSyncService interface {
	SyncAllOnRateScan(ctx context.Context)
}

func New(
	cfg config.SchedulerConfig,
	m monitoringService,
	monLogs *storage.MonitorLogs,
	syncLogs *storage.UpstreamSyncLogs,
	rates *storage.Rates,
	notifies *storage.Notifications,
	announcements *storage.UpstreamAnnouncements,
	captchas *storage.Captchas,
	cipher *crypto.Cipher,
	upstreamSync upstreamSyncService,
	proxy config.ProxyConfig,
	log *slog.Logger,
	coordinators ...*TaskRunCoordinator,
) *Scheduler {
	cfg = cfg.WithDefaults()
	if log == nil {
		log = slog.Default()
	}
	coordinator := NewTaskRunCoordinator()
	if len(coordinators) > 0 && coordinators[0] != nil {
		coordinator = coordinators[0]
	}
	return &Scheduler{
		cfg:           cfg,
		log:           log,
		cron:          cron.New(cron.WithSeconds()),
		monitor:       m,
		monLogs:       monLogs,
		syncLogs:      syncLogs,
		rates:         rates,
		notifies:      notifies,
		announcements: announcements,
		captchas:      captchas,
		cipher:        cipher,
		upstreamSync:  upstreamSync,
		proxy:         proxy,
		coordinator:   coordinator,
	}
}

// TaskRunCoordinator returns the process-level run coordinator used by this
// scheduler. Callers that rebuild a scheduler at runtime can reuse it.
func (s *Scheduler) TaskRunCoordinator() *TaskRunCoordinator {
	if s == nil {
		return nil
	}
	return s.coordinator
}

func (s *Scheduler) Start() error {
	if s.cfg.BalanceCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.BalanceCron, s.runBalance); err != nil {
			return err
		}
	}
	if s.cfg.RateCron != "" {
		if _, err := s.cron.AddFunc(s.cfg.RateCron, s.runRates); err != nil {
			return err
		}
	}
	if s.cfg.Retention.Cron != "" && s.hasRetention() {
		if _, err := s.cron.AddFunc(s.cfg.Retention.Cron, s.runRetention); err != nil {
			return err
		}
	}
	s.cron.Start()
	s.log.Info("scheduler started",
		"balanceCron", s.cfg.BalanceCron,
		"rateCron", s.cfg.RateCron,
		"retentionCron", s.cfg.Retention.Cron,
		"concurrency", s.cfg.Concurrency,
	)
	return nil
}

func (s *Scheduler) Stop() {
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
}

func (s *Scheduler) runBalance() {
	s.runProtected(TaskBalance, func() {
		s.runWithTimeout(TaskBalance, s.cfg.BalanceTimeoutSeconds, func(ctx context.Context) {
			if s.monitor != nil {
				s.monitor.ScanAllBalances(ctx)
			}
			// Do not start captcha refresh after the monitor batch consumed its
			// deadline. The task lease remains held until any in-flight work exits.
			if ctx.Err() != nil {
				return
			}
			if s.captchas != nil && s.cipher != nil {
				if _, err := captcha.RefreshAllBalancesWithProxy(ctx, s.captchas, s.cipher, s.log, s.proxy); err != nil {
					s.log.Warn("refresh captcha balances failed", "err", err)
				}
			}
		})
	})
}

func (s *Scheduler) runRates() {
	s.runProtected(TaskRates, func() {
		s.runWithTimeout(TaskRates, s.cfg.RateTimeoutSeconds, func(ctx context.Context) {
			if s.monitor != nil {
				s.monitor.ScanAllRates(ctx)
			}
			// A timed-out rate scan must not begin an additional upstream sync.
			if ctx.Err() != nil {
				return
			}
			if s.upstreamSync != nil {
				s.upstreamSync.SyncAllOnRateScan(ctx)
			}
		})
	})
}

func (s *Scheduler) hasRetention() bool {
	r := s.cfg.Retention
	return r.MonitorLogsDays > 0 ||
		r.BalanceSnapshotsDays > 0 ||
		r.NotificationLogsDays > 0 ||
		r.AnnouncementsDays > 0
}

// runRetention 按配置删除过期历史。任一表失败不影响其它，全部错误写日志。
func (s *Scheduler) runRetention() {
	s.runProtected(TaskRetention, s.runRetentionOnce)
}

func (s *Scheduler) runRetentionOnce() {
	r := s.cfg.Retention
	now := time.Now()

	if r.MonitorLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.MonitorLogsDays)
		n, err := s.monLogs.DeleteBefore(cutoff)
		if err != nil {
			s.log.Warn("retention monitor_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention monitor_logs deleted", "rows", n, "before", cutoff)
		}
		if s.syncLogs != nil {
			n, err = s.syncLogs.DeleteBefore(cutoff)
			if err != nil {
				s.log.Warn("retention upstream_sync_logs failed", "err", err)
			} else if n > 0 {
				s.log.Info("retention upstream_sync_logs deleted", "rows", n, "before", cutoff)
			}
		}
	}

	if r.BalanceSnapshotsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.BalanceSnapshotsDays)
		n, err := s.rates.DeleteBalanceSnapshotsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention balance_snapshots failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention balance_snapshots deleted", "rows", n, "before", cutoff)
		}

		n, err = s.rates.DeleteCostSnapshotsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention cost_snapshots failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention cost_snapshots deleted", "rows", n, "before", cutoff)
		}
	}

	if r.NotificationLogsDays > 0 {
		cutoff := now.AddDate(0, 0, -r.NotificationLogsDays)
		n, err := s.notifies.DeleteLogsBefore(cutoff)
		if err != nil {
			s.log.Warn("retention notification_logs failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention notification_logs deleted", "rows", n, "before", cutoff)
		}
	}

	if r.AnnouncementsDays > 0 && s.announcements != nil {
		cutoff := now.AddDate(0, 0, -r.AnnouncementsDays)
		n, err := s.announcements.DeleteBefore(cutoff)
		if err != nil {
			s.log.Warn("retention announcements failed", "err", err)
		} else if n > 0 {
			s.log.Info("retention announcements deleted", "rows", n, "before", cutoff)
		}
	}
}

func (s *Scheduler) runProtected(task TaskType, callback func()) {
	coordinator := s.coordinator
	if coordinator == nil {
		coordinator = NewTaskRunCoordinator()
		s.coordinator = coordinator
	}
	run, acquired := coordinator.TryAcquire(task)
	if !acquired {
		startedAt, _ := coordinator.ActiveSince(task)
		s.log.Warn("scheduled task skipped because previous run is active",
			"task", task,
			"running_duration", time.Since(startedAt),
			"outcome", "skipped",
		)
		return
	}
	defer run.Release()
	callback()
}

func (s *Scheduler) runWithTimeout(task TaskType, seconds int, callback func(context.Context)) {
	timeout := schedulerTaskTimeout(seconds)
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	timeoutLogged := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		s.log.Warn("scheduled task timed out",
			"task", task,
			"timeout", timeout,
			"duration", time.Since(startedAt),
			"outcome", "timed_out",
		)
		close(timeoutLogged)
	})

	callback(ctx)
	if !timer.Stop() {
		// The timer may have started logging while the callback was returning;
		// wait for that log before releasing the task lease.
		<-timeoutLogged
	}
	cancel()
}

func schedulerTaskTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = config.DefaultSchedulerTaskTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}
