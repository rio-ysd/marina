package storage

import (
	"context"
	"database/sql"
	"time"
)

// Task はタスク/TODOの1件を表します。
type Task struct {
	ID           int64
	SlackChannel string
	SlackUser    string
	Title        string
	Status       string
	DueAt        *time.Time
	CreatedAt    time.Time
}

// TaskRepo はtasksテーブルへのアクセスを提供します。
type TaskRepo struct {
	db *sql.DB
}

func NewTaskRepo(db *sql.DB) *TaskRepo {
	return &TaskRepo{db: db}
}

func (r *TaskRepo) Create(ctx context.Context, channel, user, title string, dueAt *time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO tasks (slack_channel, slack_user, title, due_at) VALUES (?, ?, ?, ?)`,
		channel, user, title, dueAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *TaskRepo) ListOpen(ctx context.Context, channel string) ([]Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, slack_channel, slack_user, title, status, due_at, created_at
		 FROM tasks WHERE slack_channel = ? AND status = 'open' ORDER BY due_at IS NULL, due_at, created_at`,
		channel,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		var dueAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.SlackChannel, &t.SlackUser, &t.Title, &t.Status, &dueAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		if dueAt.Valid {
			t.DueAt = &dueAt.Time
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) Complete(ctx context.Context, channel string, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'done' WHERE id = ? AND slack_channel = ?`,
		id, channel,
	)
	return err
}
