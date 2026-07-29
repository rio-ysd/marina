package reminderworker

import (
	"context"
	"fmt"
	"time"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/storage"
)

// Worker はDBに保存された未送信リマインダーをチェックし、期限が来たものをSlackへ送信します。
// EventBridge Scheduler等から数分おきに起動されることを想定しています。
type Worker struct {
	repo        *storage.ReminderRepo
	slackClient *slack.Client
}

func NewWorker(repo *storage.ReminderRepo, slackClient *slack.Client) *Worker {
	return &Worker{repo: repo, slackClient: slackClient}
}

// RunOnce は現在時刻までに送信すべきリマインダーをすべて送信します。
func (w *Worker) RunOnce(ctx context.Context) error {
	now := time.Now()
	due, err := w.repo.DuePending(ctx, now)
	if err != nil {
		return fmt.Errorf("list due reminders: %w", err)
	}

	for _, rem := range due {
		text := fmt.Sprintf("⏰ リマインダー: %s", rem.Message)
		if _, _, err := w.slackClient.PostMessageContext(ctx, rem.SlackChannel, slack.MsgOptionText(text, false)); err != nil {
			return fmt.Errorf("post reminder %d: %w", rem.ID, err)
		}
		if err := w.repo.MarkSent(ctx, rem.ID, now); err != nil {
			return fmt.Errorf("mark sent %d: %w", rem.ID, err)
		}
	}
	return nil
}
