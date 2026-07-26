package storage

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqliteDriver "github.com/glebarez/sqlite"
	mysqlDriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DBDriver string

const (
	DBDriverSQLite DBDriver = "sqlite"
	DBDriverMySQL  DBDriver = "mysql"
)

type DBConfig struct {
	Driver       DBDriver
	Path         string
	Host         string
	Port         int
	User         string
	Password     string
	Name         string
	MaxOpenConns int
	MaxIdleConns int
}

func (c DBConfig) SQLitePath() string {
	if strings.TrimSpace(c.Path) != "" {
		return c.Path
	}
	return "./data/upstream-ops.db"
}

func (c DBConfig) MySQLDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local", c.User, c.Password, c.Host, c.Port, c.Name)
}

func newGormLogger() logger.Interface {
	return logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		Colorful:                  true,
	})
}

func Open(cfg DBConfig) (*gorm.DB, error) {
	driver := DBDriver(strings.ToLower(string(cfg.Driver)))
	if driver == "" {
		driver = DBDriverSQLite
	}

	var dialector gorm.Dialector
	switch driver {
	case DBDriverSQLite:
		path := cfg.SQLitePath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite dir: %w", err)
		}
		dialector = sqliteDriver.Open(path)
	case DBDriverMySQL:
		dialector = mysqlDriver.Open(cfg.MySQLDSN())
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{Logger: newGormLogger()})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	if driver == DBDriverSQLite {
		if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
			return nil, fmt.Errorf("set sqlite journal mode: %w", err)
		}
		if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
			return nil, fmt.Errorf("set sqlite busy timeout: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	} else {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	return db, nil
}

// AutoMigrate initializes only the final site/account schema. The old
// channels schema has incompatible ownership semantics and must be rebuilt.
func AutoMigrate(db *gorm.DB) error {
	if db.Migrator().HasTable("channels") {
		return fmt.Errorf("detected legacy channels schema; rebuild the database before starting this version")
	}
	if err := db.AutoMigrate(
		&UpstreamSite{},
		&UpstreamAccount{},
		&AuthSession{},
		&CaptchaConfig{},
		&RateSnapshot{},
		&RateChangeLog{},
		&UpstreamAnnouncement{},
		&BalanceSnapshot{},
		&CostSnapshot{},
		&NotificationChannel{},
		&NotificationLog{},
		&NotificationCooldown{},
		&MonitorLog{},
		&UpstreamSyncTarget{},
		&UpstreamSyncTargetGroup{},
		&UpstreamSyncGroup{},
		&UpstreamSyncAccount{},
		&UpstreamSyncManagedAccount{},
		&UpstreamSyncLog{},
	); err != nil {
		return err
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_rate_account_group ON rate_snapshots (account_id, stable_group_key)").Error; err != nil {
		return fmt.Errorf("create rate account index: %w", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_announcement_site_source ON upstream_announcements (site_id, source_key)").Error; err != nil {
		return fmt.Errorf("create announcement site index: %w", err)
	}
	return validateSiteAccountIntegrity(db)
}

func validateSiteAccountIntegrity(db *gorm.DB) error {
	var accounts []UpstreamAccount
	if err := db.Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		if account.SiteID == 0 {
			return fmt.Errorf("site/account integrity: account %d has no site", account.ID)
		}
		var site UpstreamSite
		if err := db.First(&site, account.SiteID).Error; err != nil {
			return fmt.Errorf("site/account integrity: account %d site missing: %w", account.ID, err)
		}
	}
	var sites []UpstreamSite
	if err := db.Find(&sites).Error; err != nil {
		return err
	}
	for _, site := range sites {
		if _, err := NormalizeBaseURL(site.BaseURL); err != nil {
			return fmt.Errorf("site/account integrity: site %d base_url: %w", site.ID, err)
		}
		var count int64
		if err := db.Model(&UpstreamAccount{}).Where("site_id = ?", site.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if site.DefaultAccountID != 0 {
				return fmt.Errorf("site/account integrity: empty site %d has a default account", site.ID)
			}
			continue
		}
		var defaultCount int64
		if err := db.Model(&UpstreamAccount{}).Where("id = ? AND site_id = ?", site.DefaultAccountID, site.ID).Count(&defaultCount).Error; err != nil {
			return err
		}
		if defaultCount != 1 {
			return fmt.Errorf("site/account integrity: site %d default account is invalid", site.ID)
		}
	}
	return nil
}
