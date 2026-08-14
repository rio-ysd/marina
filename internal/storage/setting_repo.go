package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Setting はapp_settingsテーブルの1行(marinaの振る舞いを調整する設定値)です。
type Setting struct {
	Name      string
	Value     string
	UpdatedBy string
	UpdatedAt time.Time
}

// SettingRepo はapp_settingsテーブルへのアクセスを提供します。
type SettingRepo struct {
	db *sql.DB
}

func NewSettingRepo(db *sql.DB) *SettingRepo {
	return &SettingRepo{db: db}
}

// Get は設定値を返します。未設定の場合はゼロ値のSettingを返し、エラーにはしません
// (「まだ設定されていない」は呼び出し側にとって正常な状態のため)。
func (r *SettingRepo) Get(ctx context.Context, name string) (Setting, error) {
	s := Setting{Name: name}
	err := r.db.QueryRowContext(ctx,
		`SELECT value, updated_by, updated_at FROM app_settings WHERE name = ?`, name,
	).Scan(&s.Value, &s.UpdatedBy, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{Name: name}, nil
	}
	if err != nil {
		return Setting{}, err
	}
	return s, nil
}

// Set は設定値を保存します。既にある場合は上書きし、更新者と更新日時を記録します。
func (r *SettingRepo) Set(ctx context.Context, name, value, updatedBy string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO app_settings (name, value, updated_by) VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE value = VALUES(value), updated_by = VALUES(updated_by)`,
		name, value, updatedBy,
	)
	return err
}
