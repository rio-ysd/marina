package storage

import (
	"context"
	"database/sql"
	"time"
)

// ConversationMessage は会話履歴の1メッセージを表します。
type ConversationMessage struct {
	ID        int64
	ThreadKey string
	Role      string
	Content   string
	CreatedAt time.Time
}

// ConversationRepo はconversationsテーブルへのアクセスを提供します。
type ConversationRepo struct {
	db *sql.DB
}

func NewConversationRepo(db *sql.DB) *ConversationRepo {
	return &ConversationRepo{db: db}
}

func (r *ConversationRepo) Append(ctx context.Context, threadKey, role, content string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO conversations (thread_key, role, content) VALUES (?, ?, ?)`,
		threadKey, role, content,
	)
	return err
}

// RecentHistory はスレッドの直近limit件を古い順で返します。
func (r *ConversationRepo) RecentHistory(ctx context.Context, threadKey string, limit int) ([]ConversationMessage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, thread_key, role, content, created_at FROM (
			SELECT id, thread_key, role, content, created_at FROM conversations
			WHERE thread_key = ? ORDER BY id DESC LIMIT ?
		 ) AS recent ORDER BY id ASC`,
		threadKey, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ConversationMessage
	for rows.Next() {
		var m ConversationMessage
		if err := rows.Scan(&m.ID, &m.ThreadKey, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
