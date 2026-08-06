package storage

import (
	"context"
	"database/sql"
	"time"
)

// 代理返信下書きのステータス。
const (
	ReplyDraftStatusPending  = "pending"
	ReplyDraftStatusApproved = "approved"
	ReplyDraftStatusRejected = "rejected"
)

// ReplyDraft は「対象ユーザー宛のメンションに対してAIが作成した返信下書き」の1件を表します。
// 承認されると、対象ユーザー本人としてsource_channelのスレッドへ投稿されます。
type ReplyDraft struct {
	ID              int64
	TargetUser      string
	SourceChannel   string
	SourceThreadTS  string
	SourceMessageTS string
	RequesterUser   string
	RequestText     string
	DraftText       string
	Status          string
	ApprovalChannel string
	ApprovalTS      string
	SentTS          string
	CreatedAt       time.Time
	DecidedAt       *time.Time
}

// ReplyDraftRepo はreply_draftsテーブルへのアクセスを提供します。
type ReplyDraftRepo struct {
	db *sql.DB
}

func NewReplyDraftRepo(db *sql.DB) *ReplyDraftRepo {
	return &ReplyDraftRepo{db: db}
}

// Create は下書きをpending状態で保存し、そのIDを返します。
func (r *ReplyDraftRepo) Create(ctx context.Context, d ReplyDraft) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO reply_drafts
		   (target_user, source_channel, source_thread_ts, source_message_ts, requester_user, request_text, draft_text)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.TargetUser, d.SourceChannel, d.SourceThreadTS, d.SourceMessageTS, d.RequesterUser, d.RequestText, d.DraftText,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *ReplyDraftRepo) Get(ctx context.Context, id int64) (*ReplyDraft, error) {
	var d ReplyDraft
	var approvalChannel, approvalTS, sentTS sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, target_user, source_channel, source_thread_ts, source_message_ts, requester_user,
		        request_text, draft_text, status, approval_channel, approval_ts, sent_ts, created_at, decided_at
		 FROM reply_drafts WHERE id = ?`,
		id,
	).Scan(&d.ID, &d.TargetUser, &d.SourceChannel, &d.SourceThreadTS, &d.SourceMessageTS, &d.RequesterUser,
		&d.RequestText, &d.DraftText, &d.Status, &approvalChannel, &approvalTS, &sentTS, &d.CreatedAt, &d.DecidedAt)
	if err != nil {
		return nil, err
	}
	d.ApprovalChannel = approvalChannel.String
	d.ApprovalTS = approvalTS.String
	d.SentTS = sentTS.String
	return &d, nil
}

// SetApprovalMessage は確認DMの投稿先チャンネル/tsを記録します。
func (r *ReplyDraftRepo) SetApprovalMessage(ctx context.Context, id int64, channel, ts string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE reply_drafts SET approval_channel = ?, approval_ts = ? WHERE id = ?`,
		channel, ts, id,
	)
	return err
}

// ClaimDecision はpendingの下書きをstatusへ遷移させ、遷移できた場合のみtrueを返します。
// Slackのボタンが連打された場合やイベント再送で二重に送信されるのを防ぐための排他制御です。
func (r *ReplyDraftRepo) ClaimDecision(ctx context.Context, id int64, status string, decidedAt time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE reply_drafts SET status = ?, decided_at = ? WHERE id = ? AND status = ?`,
		status, decidedAt, id, ReplyDraftStatusPending,
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
func (r *ReplyDraftRepo) ReleaseDecision(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE reply_drafts SET status = ?, decided_at = NULL WHERE id = ?`,
		ReplyDraftStatusPending, id,
	)
	return err
}

// SetSentTS は実際に投稿されたメッセージのtsを記録します。
func (r *ReplyDraftRepo) SetSentTS(ctx context.Context, id int64, ts string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE reply_drafts SET sent_ts = ? WHERE id = ?`, ts, id)
	return err
}
