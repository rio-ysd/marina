package storage

import (
	"context"
	"database/sql"
	"time"
)

// OAuthToken は外部サービス連携用のOAuth2トークン1件を表します。
type OAuthToken struct {
	Provider     string
	Account      string
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
}

// OAuthTokenRepo はoauth_tokensテーブルへのアクセスを提供します。
type OAuthTokenRepo struct {
	db *sql.DB
}

func NewOAuthTokenRepo(db *sql.DB) *OAuthTokenRepo {
	return &OAuthTokenRepo{db: db}
}

// Upsert はprovider+accountのトークンを新規作成または更新します。
func (r *OAuthTokenRepo) Upsert(ctx context.Context, provider, account, accessToken, refreshToken string, expiresAt *time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO oauth_tokens (provider, account, access_token, refresh_token, expires_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE access_token = ?, refresh_token = ?, expires_at = ?`,
		provider, account, accessToken, refreshToken, expiresAt,
		accessToken, refreshToken, expiresAt,
	)
	return err
}

// GetByProviderAccount はprovider+accountのトークンを取得します。未認可の場合はsql.ErrNoRowsを返します。
func (r *OAuthTokenRepo) GetByProviderAccount(ctx context.Context, provider, account string) (*OAuthToken, error) {
	var t OAuthToken
	var refreshToken sql.NullString
	var expiresAt sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT provider, account, access_token, refresh_token, expires_at
		 FROM oauth_tokens WHERE provider = ? AND account = ?`,
		provider, account,
	).Scan(&t.Provider, &t.Account, &t.AccessToken, &refreshToken, &expiresAt)
	if err != nil {
		return nil, err
	}
	t.RefreshToken = refreshToken.String
	if expiresAt.Valid {
		t.ExpiresAt = &expiresAt.Time
	}
	return &t, nil
}
