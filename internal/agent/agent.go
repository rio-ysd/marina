package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/yoshida-rio/marina/internal/instructions"
	"github.com/yoshida-rio/marina/internal/storage"
	"github.com/yoshida-rio/marina/internal/tools"
)

// InstructionProvider はApp Homeで設定された追加指示の本文を返します。
// internal/instructions.Storeが実装します。取得できない場合は空文字を返します。
type InstructionProvider interface {
	Text(ctx context.Context) string
}

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

// affectionRuleTemplate は「好き」と好意を伝えられたときの返し方の指示です。
// 素の「好き」はaffectionReplyがClaudeを呼ばずに返すので、こちらは
// 「ずっと前から好きでした」のような言い回しを取りこぼさないための保険です。
// 文言が食い違わないよう、返答は判定側と同じ定数から差し込みます。
const affectionRuleTemplate = `あなたに対して「好き」と好意を伝えられた場合は、他に何も足さず「%s」とだけ返してください。`

// affectionRuleForOwner/affectionRuleForOthers は相手が本人かどうかで変わるため、
// 発話者に応じてどちらか一方だけをシステムプロンプトへ差し込みます。
var (
	affectionRuleForOwner  = fmt.Sprintf(affectionRuleTemplate, affectionReplyForOwner)
	affectionRuleForOthers = fmt.Sprintf(affectionRuleTemplate, affectionReplyForOthers)
)

// jst は「今月」「来月」を解決するための基準タイムゾーンです(LambdaのTZはUTCのため明示)。
var jst = time.FixedZone("JST", 9*60*60)

// systemPromptWithDate は現在日付とApp Homeで設定された追加指示を添えたシステムプロンプトを返します。
// 日付が無いとClaudeは「今月」「来月」を解決できず、ツールを無駄に呼び続けてしまいます。
// speakerIsOwnerは発話者が本人かどうかで、好意を伝えられたときの返し方を切り替えるのに使います。
// customが空の場合は何も足しません。
func systemPromptWithDate(now time.Time, custom string, speakerIsOwner bool) string {
	affection := affectionRuleForOthers
	if speakerIsOwner {
		affection = affectionRuleForOwner
	}
	return fmt.Sprintf("%s\n\n本日は %s です(JST)。「今月」「来月」「先週」などの相対的な期間はこの日付を基準に解釈してください。\n%s%s",
		systemPrompt, now.In(jst).Format("2006年1月2日(Mon)"), affection, instructions.PromptSection(custom))
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
	instructions InstructionProvider
	// ownerUserID は本人(PROXY_REPLY_TARGET_USER_ID)のSlackユーザーID。空なら本人不在として扱います。
	ownerUserID string
}

// New はAgentを構築します。toolsにはtask/reminder/gmail/mfinvoice等の全ツールを渡します。
// instructionsはApp Homeで設定された追加指示の供給元です(nil可)。
// ownerUserIDは本人のSlackユーザーID(PROXY_REPLY_TARGET_USER_ID)です(空可)。
func New(client anthropic.Client, model string, allTools []anthropic.BetaTool, conversation *storage.ConversationRepo, customInstructions InstructionProvider, ownerUserID string) *Agent {
	return &Agent{
		client:       client,
		model:        anthropic.Model(model),
		tools:        allTools,
		conversation: conversation,
		instructions: customInstructions,
		ownerUserID:  ownerUserID,
	}
}

// isOwner は発話者が本人(PROXY_REPLY_TARGET_USER_ID)かを返します。
// 未設定のときは誰も本人と見なしません(相手を取り違えて親密な応答を返さないため)。
func (a *Agent) isOwner(user string) bool {
	return a.ownerUserID != "" && user == a.ownerUserID
}

// customInstructions はApp Homeで設定された追加指示を返します。未設定なら空文字です。
func (a *Agent) customInstructions(ctx context.Context) string {
	if a.instructions == nil {
		return ""
	}
	return a.instructions.Text(ctx)
}

// Respond はSlackから受け取ったユーザー発話に対する応答テキストを生成します。
// threadKeyは会話履歴を紐づけるキー(例: "channel:thread_ts")、channel/userはSlackのタスクツールで使う識別子です。
func (a *Agent) Respond(ctx context.Context, threadKey, channel, user, userText string) (string, error) {
	// 「好き」への返答は固定文言なので、Claudeを呼ばずにその場で返す。
	if reply, ok := affectionReply(userText, a.isOwner(user)); ok {
		return reply, a.saveExchange(ctx, threadKey, userText, reply)
	}

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
			System:    []anthropic.BetaTextBlockParam{{Text: systemPromptWithDate(time.Now(), a.customInstructions(ctx), a.isOwner(user))}},
			Messages:  messages,
		},
		MaxIterations: maxToolIterations,
	})

	final, err := runner.RunToCompletion(callCtx)
	if err != nil {
		return "", fmt.Errorf("run to completion: %w", err)
	}

	replyText := replyOrFallback(final)

	if err := a.saveExchange(ctx, threadKey, userText, replyText); err != nil {
		return "", err
	}

	return replyText, nil
}

// saveExchange はユーザー発話とmarinaの応答を会話履歴に残します。
// Claudeを呼ばずに返した応答も、後続のやりとりで文脈が飛ばないよう同じように保存します。
func (a *Agent) saveExchange(ctx context.Context, threadKey, userText, replyText string) error {
	if err := a.conversation.Append(ctx, threadKey, "user", userText); err != nil {
		return fmt.Errorf("save user message: %w", err)
	}
	if err := a.conversation.Append(ctx, threadKey, "assistant", replyText); err != nil {
		return fmt.Errorf("save assistant message: %w", err)
	}
	return nil
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
