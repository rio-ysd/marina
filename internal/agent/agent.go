package agent

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

const systemPrompt = `あなたは日本語で対応する優秀な秘書AIエージェント「marina」です。
Slack上でユーザーの秘書として、スケジュール/リマインダー管理、タスク管理、雑務の相談、
メール対応やMoneyForwardの見積書・請求書作成の補助を行います。
ユーザーからの依頼に対して、必要に応じて提供されているツールを使い、簡潔で丁寧な日本語で応答してください。
見積書・請求書を作成する際は、先にmf_search_partnersで取引先のdepartment_idを確認してから作成ツールを呼んでください。
請求書・見積書の件数や一覧を聞かれたらmf_list_invoices/mf_list_estimatesを使います。
期間を省略すると今月(JST)が対象になるので、「今月」ならfrom/toを指定せずに呼んでください。
件数を答えるときは一覧の行数ではなく、ツールが返す総件数を使ってください。`

const maxHistoryMessages = 20
const maxToolIterations = 8

// Agent はClaude APIを使った秘書エージェントのコア処理を提供します。
type Agent struct {
	client       anthropic.Client
	model        anthropic.Model
	tools        []anthropic.BetaTool
	conversation *storage.ConversationRepo
}

// New はAgentを構築します。toolsにはtask/reminder/gmail/mfinvoice等の全ツールを渡します。
func New(client anthropic.Client, model string, allTools []anthropic.BetaTool, conversation *storage.ConversationRepo) *Agent {
	return &Agent{
		client:       client,
		model:        anthropic.Model(model),
		tools:        allTools,
		conversation: conversation,
	}
}

// Respond はSlackから受け取ったユーザー発話に対する応答テキストを生成します。
// threadKeyは会話履歴を紐づけるキー(例: "channel:thread_ts")、channel/userはSlackのタスクツールで使う識別子です。
func (a *Agent) Respond(ctx context.Context, threadKey, channel, user, userText string) (string, error) {
	history, err := a.conversation.RecentHistory(ctx, threadKey, maxHistoryMessages)
	if err != nil {
		return "", fmt.Errorf("load history: %w", err)
	}

	messages := make([]anthropic.BetaMessageParam, 0, len(history)+1)
	for _, h := range history {
		block := anthropic.NewBetaTextBlock(h.Content)
		switch h.Role {
		case "user":
			messages = append(messages, anthropic.NewBetaUserMessage(block))
		case "assistant":
			messages = append(messages, anthropic.BetaMessageParam{
				Role:    anthropic.BetaMessageParamRoleAssistant,
				Content: []anthropic.BetaContentBlockParamUnion{block},
			})
		}
	}
	messages = append(messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(userText)))

	callCtx := tools.WithTaskCallCtx(ctx, tools.TaskCallCtx{SlackChannel: channel, SlackUser: user})

	runner := a.client.Beta.Messages.NewToolRunner(a.tools, anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:     a.model,
			MaxTokens: 2048,
			System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
			Messages:  messages,
		},
		MaxIterations: maxToolIterations,
	})

	final, err := runner.RunToCompletion(callCtx)
	if err != nil {
		return "", fmt.Errorf("run to completion: %w", err)
	}

	replyText := extractText(final)

	if err := a.conversation.Append(ctx, threadKey, "user", userText); err != nil {
		return "", fmt.Errorf("save user message: %w", err)
	}
	if err := a.conversation.Append(ctx, threadKey, "assistant", replyText); err != nil {
		return "", fmt.Errorf("save assistant message: %w", err)
	}

	return replyText, nil
}

func extractText(msg *anthropic.BetaMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range msg.Content {
		if tb, ok := c.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
