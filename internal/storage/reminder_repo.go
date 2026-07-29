package storage

import (
	"context"
	"database/sql"
	"time"
)

// Reminder はリマインダーの1件を表します。
type Reminder struct {
	ID           int64
	SlackChannel string
	SlackUser    string
	Message      string
	RemindAt     time.Time
	SentAt       *time.Time
}

// ReminderRepo はremindersテーブルへのアクセスを提供します。
type ReminderRepo struct {
	db *sql.DB
}

func NewReminderRepo(db *sql.DB) *ReminderRepo {
	return &ReminderRepo{db: db}
}

func (r *ReminderRepo) Create(ctx context.Context, channel, user, message string, remindAt time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO reminders (slack_channel, slack_user, message, remind_at) VALUES (?, ?, ?, ?)`,
		channel, user, message, remindAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DuePending は現在時刻までに送信すべき未送信リマインダーを返します。
func (r *ReminderRepo) DuePending(ctx context.Context, now time.Time) ([]Reminder, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, slack_channel, slack_user, message, remind_at
		 FROM reminders WHERE sent_at IS NULL AND remind_at <= ? ORDER BY remind_at`,
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reminders []Reminder
	for rows.Next() {
		var rem Reminder
		if err := rows.Scan(&rem.ID, &rem.SlackChannel, &rem.SlackUser, &rem.Message, &rem.RemindAt); err != nil {
			return nil, err
		}
		reminders = append(reminders, rem)
	}
	return reminders, rows.Err()
}

func (r *ReminderRepo) MarkSent(ctx context.Context, id int64, sentAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE reminders SET sent_at = ? WHERE id = ?`, sentAt, id)
	return err
}
