package storage

import (
	"context"
	"database/sql"
	"time"
)

// アカウント作成リクエストのステータス。
const (
	UserCreationStatusPending  = "pending"
	UserCreationStatusApproved = "approved"
	UserCreationStatusRejected = "rejected"
)

// UserCreationRequest は「Google Workspaceアカウントを作ってほしい」という承認待ちの依頼1件です。
// 一時パスワードは保存しません(作成時に生成し、承認者へのDMで一度だけ通知します)。
type UserCreationRequest struct {
	ID              int64
	RequesterUser   string
	RequestChannel  string
	PrimaryEmail    string
	GivenName       string
	FamilyName      string
	OrgUnitPath     string
	Status          string
	ApprovalChannel string
	ApprovalTS      string
	CreatedEmail    string
	CreatedAt       time.Time
	DecidedAt       *time.Time
}

// UserCreationRequestRepo はuser_creation_requestsテーブルへのアクセスを提供します。
type UserCreationRequestRepo struct {
	db *sql.DB
}

func NewUserCreationRequestRepo(db *sql.DB) *UserCreationRequestRepo {
	return &UserCreationRequestRepo{db: db}
}

// Create は依頼をpending状態で保存し、そのIDを返します。
func (r *UserCreationRequestRepo) Create(ctx context.Context, req UserCreationRequest) (int64, error) {
	var orgUnitPath any
	if req.OrgUnitPath != "" {
		orgUnitPath = req.OrgUnitPath
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO user_creation_requests
		   (requester_user, request_channel, primary_email, given_name, family_name, org_unit_path)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		req.RequesterUser, req.RequestChannel, req.PrimaryEmail, req.GivenName, req.FamilyName, orgUnitPath,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *UserCreationRequestRepo) Get(ctx context.Context, id int64) (*UserCreationRequest, error) {
	var req UserCreationRequest
	var orgUnitPath, approvalChannel, approvalTS, createdEmail sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, requester_user, request_channel, primary_email, given_name, family_name, org_unit_path,
		        status, approval_channel, approval_ts, created_email, created_at, decided_at
		 FROM user_creation_requests WHERE id = ?`,
		id,
	).Scan(&req.ID, &req.RequesterUser, &req.RequestChannel, &req.PrimaryEmail, &req.GivenName, &req.FamilyName,
		&orgUnitPath, &req.Status, &approvalChannel, &approvalTS, &createdEmail, &req.CreatedAt, &req.DecidedAt)
	if err != nil {
		return nil, err
	}
	req.OrgUnitPath = orgUnitPath.String
	req.ApprovalChannel = approvalChannel.String
	req.ApprovalTS = approvalTS.String
	req.CreatedEmail = createdEmail.String
	return &req, nil
}

// SetApprovalMessage は確認DMの投稿先チャンネル/tsを記録します。
func (r *UserCreationRequestRepo) SetApprovalMessage(ctx context.Context, id int64, channel, ts string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE user_creation_requests SET approval_channel = ?, approval_ts = ? WHERE id = ?`,
		channel, ts, id,
	)
	return err
}

// ClaimDecision はpendingの依頼をstatusへ遷移させ、遷移できた場合のみtrueを返します。
// ボタン連打やイベント再送で同じアカウントが二重に作られるのを防ぐための排他制御です。
func (r *UserCreationRequestRepo) ClaimDecision(ctx context.Context, id int64, status string, decidedAt time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE user_creation_requests SET status = ?, decided_at = ? WHERE id = ? AND status = ?`,
		status, decidedAt, id, UserCreationStatusPending,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// ReleaseDecision はClaimDecision後の処理が失敗した場合にpendingへ戻します。
func (r *UserCreationRequestRepo) ReleaseDecision(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE user_creation_requests SET status = ?, decided_at = NULL WHERE id = ?`,
		UserCreationStatusPending, id,
	)
	return err
}

// SetCreatedEmail は実際に作成されたアカウントのメールアドレスを記録します。
func (r *UserCreationRequestRepo) SetCreatedEmail(ctx context.Context, id int64, email string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE user_creation_requests SET created_email = ? WHERE id = ?`, email, id)
	return err
}
