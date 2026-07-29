package tools

import (
	"context"
	"fmt"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"

	"github.com/yoshida-rio/marina/internal/storage"
)

// NewReminderTools はリマインダー管理用のBetaToolセットを構築します。
func NewReminderTools(repo *storage.ReminderRepo) ([]anthropic.BetaTool, error) {
	createTool, err := toolrunner.NewBetaToolFromBytes[createReminderInput](
		"create_reminder",
		"指定した日時にSlackへ通知するリマインダーを作成する。remind_atはRFC3339形式(例: 2026-08-01T09:00:00+09:00)で渡す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message":   map[string]any{"type": "string", "description": "リマインド内容"},
				"remind_at": map[string]any{"type": "string", "description": "通知日時(RFC3339形式)"},
			},
			"required": []string{"message", "remind_at"},
		}),
		func(ctx context.Context, in createReminderInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			callCtx, err := taskCallCtxFrom(ctx)
			if err != nil {
				return textResult(""), err
			}
			remindAt, err := time.Parse(time.RFC3339, in.RemindAt)
			if err != nil {
				return textResult(fmt.Sprintf("remind_atの形式が正しくありません: %v", err)), nil
			}
			id, err := repo.Create(ctx, callCtx.SlackChannel, callCtx.SlackUser, in.Message, remindAt)
			if err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("リマインダーを作成しました (id=%d): %s を %s に通知します。", id, in.Message, remindAt.Format(time.RFC3339))), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{createTool}, nil
}

type createReminderInput struct {
	Message  string `json:"message"`
	RemindAt string `json:"remind_at"`
}
