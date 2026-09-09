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
監視カメラの通知が何時に届いたか聞かれたらget_camera_notification_time_rangeを使い、日付はYYYY-MM-DD(JST)で渡してください。
「〇〇さんはシフト時間外に働いているか」のような依頼はdql_check_shift_complianceを使ってください
(氏名解決・UTC/JST変換・シフトとのJOINをツール内で完結させるため、db_select_queryで都度組み立てるより速く確実です)。
対象者がdqlではニックネーム(例: MAHO)で呼ばれている場合、beryx_productionには本名(例: 鈴木瑞希)で登録されているため、
本名が分かっていればberyx_nameに渡してください(省略するとnameと同じ値で検索し、見つからなければ本名を尋ねてください)。
シフト時間外の実働に加えて防犯センサー(監視カメラ)の検知時刻も合わせて知りたい場合はcheck_staff_attendance_summaryを
使ってください(引数はdql_check_shift_complianceと同じname/beryx_name/from/to)。複数人分をまとめて聞かれた場合は
人ごとに呼び出し、結果を1つのSlackメッセージにまとめてください。
check_staff_attendance_summaryの結果は、時刻や差分を自分で計算し直したり要約したりせず、ツールが返したテキストを
インデント付き箇条書きの構造そのままで出力してください(数値の書き換え・言い換えは誤りのもとになります)。
複数人分をまとめる場合も、各ツール呼び出しの出力をそのまま連結するだけにしてください。
**最重要**: check_staff_attendance_summaryとdql_check_shift_complianceが返す時刻・時間差・日付は、
一字一句そのまま転記してください。文章を書き直す過程で数値を推測・再計算・丸め直すことは絶対にしないでください。
自分の暗算や記憶ではなくツールの出力そのものが常に正しいので、ツールの文字列をコピーするように扱ってください。
DBの中身を直接確認したい場合はdb_select_queryを使います(SELECT文のみ、最大200行)。テーブル構造が不明ならinformation_schema.columnsを先に調べてください。
db_select_queryは複数のDBに接続できるため、テーブル名は必ず「データベース名.テーブル名」(例: dql.shifts, beryx_production.projects)で完全修飾してください。
重要: dqlのDATETIME列(reservation_at, entered_at, left_at, created_at等)はUTCで保存されています。
JSTの時刻と比較・表示する際は必ずDATE_ADD(列名, INTERVAL 9 HOUR)で変換してください(変換を忘れると実際より9時間早い時刻に見えます)。
一方dql.shifts.shift_hourとberyx_production.reportsのDATETIME列(started_at, ended_at)はJSTでそのまま保存されており変換不要です。
「dql」はサロン予約管理システムのDBです。主なテーブルの知識:
- dql.shifts: shift_date(YYYY-MM-DD文字列)とshift_hour(その日0時からの分数。例: 540=9:00, 570=9:30)の組み合わせが
  30分単位のシフト1コマを表す。1人のスタッフの1日のシフトは複数行になる。user_idでdql.usersに紐づく。
- dql.users: name(名)/last_name(姓)を持つ。スタッフも顧客もこのテーブルに入るが、**スタッフ本人(対象ユーザー)と
  言えるのはdql.adminsにレコードがあるユーザーのみ**。「〇〇さんのシフト」のようにスタッフを指す依頼では、
  dql.usersを単独で検索せずdql.adminsとJOINして絞り込んでください(顧客が同姓同名でヒットするのを防ぐため)。
  姓だけで検索すると同姓の別人が複数ヒットすることがあるため、該当者が複数いる場合は下の名前を確認するか候補を提示してから答えてください。
- dql.admins: user_idでusersと1:1。slack_user_id(Slackユーザーの識別子)やrole(役割)を持ち、スタッフ判定に使う。
- dql.reservations: 予約。staff_user_id(担当スタッフ)/reservation_at(UTC)/status(1予約中/2完了/3キャンセル/
  4キャンセル無断/5キャンセル予定変更/6キャンセル店都合)を持つ。
- dql.user_store_presences: 入退店ログ。user_id/entered_at/left_at(いずれもUTC)。entered_atとleft_atの間隔が
  極端に長い(1日を大きく超える)行は打刻漏れの可能性が高く、実働時間として信頼しない。
「beryx_production」は勤怠管理システムのDBです。主なテーブルの知識:
- beryx_production.users: 社員。id/name/email/join_company_at(入社日)を持つ。dqlのスタッフも別IDでここに登録されている。
- beryx_production.projects: 案件。client_id/name/started_at/ended_at/budgetなどを持つ。
- beryx_production.members: usersとprojectsの中間テーブル(アサイン)。user_id/project_id/assigned_at/completed_atを持つ。
- beryx_production.reports: **実際の稼働時間の記録**。member_idで紐づき、started_at/ended_at(JSTでそのまま保存、
  UTC変換不要)/rest_time(休憩時間)を持つ。「シフト時間外に働いているか」を調べるときは、dql.shifts(シフト予定)と
  このreports(全プロジェクト合算の実働)を突き合わせる(dql.reservations/user_store_presencesは実働の判定には使わない)。
出力先はSlackなので、太字は**text**ではなく*text*、リンクは<URL|表示文字>の記法を使ってください。見出し記法(#)は使えません。`

// jst は「今月」「来月」を解決するための基準タイムゾーンです(LambdaのTZはUTCのため明示)。
var jst = time.FixedZone("JST", 9*60*60)

// systemPromptWithDate は現在日付とApp Homeで設定された追加指示を添えたシステムプロンプトを返します。
// 日付が無いとClaudeは「今月」「来月」を解決できず、ツールを無駄に呼び続けてしまいます。
// customが空の場合は何も足しません。
func systemPromptWithDate(now time.Time, custom string) string {
	return fmt.Sprintf("%s\n\n本日は %s です(JST)。「今月」「来月」「先週」などの相対的な期間はこの日付を基準に解釈してください。%s",
		systemPrompt, now.In(jst).Format("2006年1月2日(Mon)"), instructions.PromptSection(custom))
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
			System:    []anthropic.BetaTextBlockParam{{Text: systemPromptWithDate(time.Now(), a.customInstructions(ctx))}},
			Messages:  messages,
		},
		MaxIterations: maxToolIterations,
	})

	final, err := runner.RunToCompletion(callCtx)
	if err != nil {
		return "", fmt.Errorf("run to completion: %w", err)
	}

	replyText := replyOrFallback(final)
	// 数値を含むツール結果はClaudeが言い換える過程で書き換えてしまうことがあるため、
	// 該当ツールが呼ばれていた場合は生成された文章を使わず、ツールの出力をそのまま返信にする。
	if verbatim := extractVerbatimToolResults(runner.Messages()); len(verbatim) > 0 {
		replyText = strings.Join(verbatim, "\n")
	}

	if err := a.saveExchange(ctx, threadKey, userText, replyText); err != nil {
		return "", err
	}

	return replyText, nil
}

// verbatimToolNames はここに列挙したツールの結果を、Claudeの言い換えを介さずそのままSlackへの返信に使います。
// 時刻・時間差など数値を含む結果をLLMが文章に組み込む過程で書き換えてしまう事例が確認されたための対策です。
var verbatimToolNames = map[string]bool{
	"check_staff_attendance_summary": true,
	"dql_check_shift_compliance":     true,
}

// extractVerbatimToolResults はrunnerの会話履歴から、verbatimToolNamesに該当するtool_resultの本文を
// 呼び出し順に取り出します(該当が無ければ空を返します)。
func extractVerbatimToolResults(messages []anthropic.BetaMessageParam) []string {
	toolNameByID := map[string]string{}
	for _, m := range messages {
		if m.Role != anthropic.BetaMessageParamRoleAssistant {
			continue
		}
		for _, c := range m.Content {
			if c.OfToolUse != nil {
				toolNameByID[c.OfToolUse.ID] = c.OfToolUse.Name
			}
		}
	}

	var results []string
	for _, m := range messages {
		if m.Role != anthropic.BetaMessageParamRoleUser {
			continue
		}
		for _, c := range m.Content {
			if c.OfToolResult == nil || !verbatimToolNames[toolNameByID[c.OfToolResult.ToolUseID]] {
				continue
			}
			for _, content := range c.OfToolResult.Content {
				if content.OfText != nil {
					results = append(results, stripVerbatimNotice(content.OfText.Text))
				}
			}
		}
	}
	return results
}

// stripVerbatimNotice はtools.verbatimResultが先頭に付ける「重要: ...」の注意書きを取り除きます
// (Claude向けの指示であり、Slackへの返信本文には不要なため)。
func stripVerbatimNotice(text string) string {
	if !strings.HasPrefix(text, "重要:") {
		return text
	}
	if idx := strings.Index(text, "\n\n"); idx != -1 {
		return text[idx+len("\n\n"):]
	}
	return text
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
