package storage

import (
	"errors"

	"gorm.io/gorm"
)

type AuthSessions struct{ db *gorm.DB }

func NewAuthSessions(db *gorm.DB) *AuthSessions { return &AuthSessions{db: db} }

// WithDB returns a repository bound to db, typically a transaction handle.
func (r *AuthSessions) WithDB(db *gorm.DB) *AuthSessions { return NewAuthSessions(db) }

// FindByAccount returns an account session, or (nil, nil) when none exists.
func (r *AuthSessions) FindByAccount(accountID uint) (*AuthSession, error) {
	var s AuthSession
	err := r.db.First(&s, "account_id = ?", accountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Upsert 写入或更新会话。
func (r *AuthSessions) Upsert(s *AuthSession) error {
	var existing AuthSession
	err := r.db.First(&existing, "account_id = ?", s.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(s).Error
	}
	if err != nil {
		return err
	}
	return r.db.Save(s).Error
}

// Delete removes an account session.
func (r *AuthSessions) Delete(accountID uint) error {
	return r.db.Delete(&AuthSession{}, "account_id = ?", accountID).Error
}
