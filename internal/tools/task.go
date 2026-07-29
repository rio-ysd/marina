package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"

	"github.com/yoshida-rio/marina/internal/storage"
)

// TaskCallCtx はタスク系ツールが実行時に必要とするSlackコンテキストです。
// agentパッケージからcontext.Contextの値として渡されます。
type TaskCallCtx struct {
	SlackChannel string
	SlackUser    string
}

type ctxKeyTaskCall struct{}

// WithTaskCallCtx はcontextにSlackコンテキストを埋め込みます。
func WithTaskCallCtx(ctx context.Context, c TaskCallCtx) context.Context {
	return context.WithValue(ctx, ctxKeyTaskCall{}, c)
}

func taskCallCtxFrom(ctx context.Context) (TaskCallCtx, error) {
	c, ok := ctx.Value(ctxKeyTaskCall{}).(TaskCallCtx)
	if !ok {
		return TaskCallCtx{}, fmt.Errorf("slack context not found")
	}
	return c, nil
}

func textResult(s string) anthropic.BetaToolResultBlockParamContentUnion {
	return anthropic.BetaToolResultBlockParamContentUnion{
		OfText: &anthropic.BetaTextBlockParam{Text: s},
	}
}

// NewTaskTools はタスク管理用のBetaToolセットを構築します。
func NewTaskTools(repo *storage.TaskRepo) ([]anthropic.BetaTool, error) {
	createTool, err := toolrunner.NewBetaToolFromBytes[createTaskInput](
		"create_task",
		"新しいタスク/TODOを作成する。dueは省略可能で、指定する場合はRFC3339形式(例: 2026-08-01T18:00:00+09:00)で渡す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "タスクの内容"},
				"due":   map[string]any{"type": "string", "description": "期限(RFC3339形式、任意)"},
			},
			"required": []string{"title"},
		}),
		func(ctx context.Context, in createTaskInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			callCtx, err := taskCallCtxFrom(ctx)
			if err != nil {
				return textResult(""), err
			}
			var dueAt *time.Time
			if strings.TrimSpace(in.Due) != "" {
				t, err := time.Parse(time.RFC3339, in.Due)
				if err != nil {
					return textResult(fmt.Sprintf("期限の形式が正しくありません: %v", err)), nil
				}
				dueAt = &t
			}
			id, err := repo.Create(ctx, callCtx.SlackChannel, callCtx.SlackUser, in.Title, dueAt)
			if err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("タスクを作成しました (id=%d): %s", id, in.Title)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	listTool, err := toolrunner.NewBetaToolFromBytes[listTaskInput](
		"list_tasks",
		"このSlackチャンネルの未完了タスク一覧を取得する。",
		mustSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, _ listTaskInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			callCtx, err := taskCallCtxFrom(ctx)
			if err != nil {
				return textResult(""), err
			}
			tasks, err := repo.ListOpen(ctx, callCtx.SlackChannel)
			if err != nil {
				return textResult(""), err
			}
			if len(tasks) == 0 {
				return textResult("未完了のタスクはありません。"), nil
			}
			b, err := json.Marshal(tasks)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	completeTool, err := toolrunner.NewBetaToolFromBytes[completeTaskInput](
		"complete_task",
		"指定したIDのタスクを完了にする。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "タスクID"},
			},
			"required": []string{"id"},
		}),
		func(ctx context.Context, in completeTaskInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			callCtx, err := taskCallCtxFrom(ctx)
			if err != nil {
				return textResult(""), err
			}
			if err := repo.Complete(ctx, callCtx.SlackChannel, in.ID); err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("タスク(id=%d)を完了にしました。", in.ID)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{createTool, listTool, completeTool}, nil
}

type createTaskInput struct {
	Title string `json:"title"`
	Due   string `json:"due,omitempty"`
}

type listTaskInput struct{}

type completeTaskInput struct {
	ID int64 `json:"id"`
}

func mustSchema(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}
