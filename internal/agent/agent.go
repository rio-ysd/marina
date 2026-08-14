package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

const systemPrompt = `あなたは日本語で対応する優秀な秘書AIエージェント「marina」です。
Slack上でユーザーの秘書として、スケジュール/リマインダー管理、タスク管理、雑務の相談、
メール対応、Googleカレンダーの予定確認、Google Driveの資料探し、スプレッドシートの読み書き、
MoneyForwardの見積書・請求書作成の補助を行います。
ユーザーからの依頼に対して、必要に応じて提供されているツールを使い、簡潔で丁寧な日本語で応答してください。
見積書・請求書を作成する際は、先にmf_search_partnersで取引先のdepartment_idを確認してから作成ツールを呼んでください。
請求書・見積書の件数や一覧を聞かれたらmf_list_invoices/mf_list_estimatesを使います。
期間を省略すると今月(JST)が対象になるので、「今月」ならfrom/toを指定せずに呼んでください。
件数を答えるときは一覧の行数ではなく、ツールが返す総件数を使ってください。
「支払期限が今月」「今月末までに入金予定」のように支払期限で聞かれた場合はdate_typeにdue_dateを指定します
(請求日で絞ると別の結果になるため、どちらを聞かれているか区別してください)。
Google Driveの資料を探すときはdrive_search_filesを使い、中身を確認する必要があるときだけdrive_read_fileで本文を読みます。
資料の場所を答えるときは、ファイル名だけでなくリンクも添えてください。
drive_read_fileで読めない形式(PDF・画像・Officeファイル)はリンクの案内にとどめ、内容を推測で答えないでください。
保存先フォルダを指定してドキュメントを作る場合は、先にdrive_search_files(file_type=folder)でfolder_idを確認してください。
予定を聞かれたらcalendar_list_eventsを使います。期間を省略すると今日(JST)が対象なので、「今日の予定」ならfrom/toを指定せずに呼んでください。
calendar_create_eventは自分のカレンダーに予定を入れるだけで、他の参加者は招待できません。参加者が必要な場合は予定を作ったうえでその旨を伝えてください。
スプレッドシートを扱うときは、drive_search_files(file_type=spreadsheet)でspreadsheet_idを探し、
シート名が不明ならsheets_get_infoで確認してからsheets_read_rangeで読みます。
sheets_append_rowsは末尾への追記のみで既存セルは書き換えられません。追記する前にsheets_read_rangeで見出し行を確認し、列の順番を合わせてください。
人のメールアドレスや所属を聞かれたら、社内はpeople_search_directory、社外はpeople_search_contactsで調べます。見つからなければ推測せず、見つからなかったと答えてください。
アカウント作成を頼まれた場合、directory_request_user_creationは承認依頼を送るだけでアカウントは作られません。
「承認依頼を送りました。承認されると作成されます」と伝え、作成済みとは絶対に言わないでください。
出力先はSlackなので、太字は**text**ではなく*text*、リンクは<URL|表示文字>の記法を使ってください。見出し記法(#)は使えません。`

// jst は「今月」「来月」を解決するための基準タイムゾーンです(LambdaのTZはUTCのため明示)。
var jst = time.FixedZone("JST", 9*60*60)

// systemPromptWithDate は現在日付を添えたシステムプロンプトを返します。
// 日付が無いとClaudeは「今月」「来月」を解決できず、ツールを無駄に呼び続けてしまいます。
func systemPromptWithDate(now time.Time) string {
	return fmt.Sprintf("%s\n\n本日は %s です(JST)。「今月」「来月」「先週」などの相対的な期間はこの日付を基準に解釈してください。",
		systemPrompt, now.In(jst).Format("2006年1月2日(Mon)"))
}

// replyOrFallback は空の応答をそのまま返さないようにします。
// ツール実行の反復上限に達すると最終メッセージがtool_useブロックだけになり本文が空になります。
// これをそのままSlackへ投稿するとno_textエラーで送信自体が失敗し、ユーザーには何も届きません。
func replyOrFallback(msg *anthropic.BetaMessage) string {
	if text := strings.TrimSpace(extractText(msg)); text != "" {
		return text
	}
	var stop anthropic.BetaStopReason
	if msg != nil {
		stop = msg.StopReason
	}
	log.Printf("agent: empty reply text (stop_reason=%q)", stop)
	if stop == anthropic.BetaStopReasonToolUse {
		return "処理の途中で内部処理の上限に達しました。対象を絞る(期間や件数を指定する)ともう一度試せます。"
	}
	return "うまく回答を作れませんでした。表現を変えてもう一度お願いします。"
}

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
			System:    []anthropic.BetaTextBlockParam{{Text: systemPromptWithDate(time.Now())}},
			Messages:  messages,
		},
		MaxIterations: maxToolIterations,
	})

	final, err := runner.RunToCompletion(callCtx)
	if err != nil {
		return "", fmt.Errorf("run to completion: %w", err)
	}

	replyText := replyOrFallback(final)

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
