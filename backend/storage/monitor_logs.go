package storage

import (
	"time"

	"gorm.io/gorm"
)

type MonitorLogs struct{ db *gorm.DB }

func NewMonitorLogs(db *gorm.DB) *MonitorLogs { return &MonitorLogs{db: db} }

func (r *MonitorLogs) Append(l *MonitorLog) error {
	if l.StartedAt.IsZero() {
		l.StartedAt = time.Now()
	}
	if l.FinishedAt.IsZero() {
		l.FinishedAt = time.Now()
	}
	if l.DurationMS == 0 {
		l.DurationMS = l.FinishedAt.Sub(l.StartedAt).Milliseconds()
	}
	return r.db.Create(l).Error
}

// List returns monitor logs in reverse chronological order. accountID 0 skips filtering.
func (r *MonitorLogs) List(accountID uint, limit int) ([]MonitorLog, error) {
	if limit <= 0 {
		limit = 100
	}
	q := r.db.Order("started_at DESC").Limit(limit)
	if accountID != 0 {
		q = q.Where("account_id = ?", accountID)
	}
	var list []MonitorLog
	if err := q.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DeleteBefore 删除 started_at < cutoff 的日志，返回删除行数。
func (r *MonitorLogs) DeleteBefore(cutoff time.Time) (int64, error) {
	res := r.db.Where("started_at < ?", cutoff).Delete(&MonitorLog{})
	return res.RowsAffected, res.Error
}
