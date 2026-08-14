package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// EventTime は予定の開始/終了時刻です。終日予定はDateのみ、時刻付きはDateTimeのみを持ちます。
// Claudeからは文字列で受け取り、parseEventTimeでこの形に正規化します。
type EventTime struct {
	// Date は終日予定の日付(YYYY-MM-DD)。
	Date string
	// DateTime は時刻付き予定の日時(RFC3339、JSTオフセット付き)。
	DateTime string
}

// IsAllDay は終日予定かどうかを返します。
func (t EventTime) IsAllDay() bool { return t.Date != "" }

// CalendarEvent はGoogleカレンダーの予定です。
type CalendarEvent struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Start/End は表示用の文字列(終日予定はYYYY-MM-DD、時刻付きはRFC3339)。
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url,omitempty"`
}

const (
	calendarDateTimeLayout = "2006-01-02T15:04"
	// calendarDateTimeLayoutSpace は「2026-08-14 10:00」のようにTの代わりに空白で来た場合の受け皿です。
	calendarDateTimeLayoutSpace = "2006-01-02 15:04"
	calendarDateTimeSecLayout   = "2006-01-02T15:04:05"
)

// parseEventTime はClaudeが渡す日時文字列をEventTimeへ正規化します。
// 受け付ける形式: YYYY-MM-DD(終日)、YYYY-MM-DDTHH:MM、YYYY-MM-DD HH:MM、YYYY-MM-DDTHH:MM:SS、RFC3339。
// タイムゾーンの指定がないものはJSTとして解釈します(LambdaのTZはUTCなので明示が必要)。
func parseEventTime(v string) (EventTime, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return EventTime{}, fmt.Errorf("日時が空です")
	}

	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return EventTime{DateTime: t.In(jst).Format(time.RFC3339)}, nil
	}
	if t, err := time.ParseInLocation(mfDateLayout, v, jst); err == nil {
		return EventTime{Date: t.Format(mfDateLayout)}, nil
	}
	for _, layout := range []string{calendarDateTimeLayout, calendarDateTimeLayoutSpace, calendarDateTimeSecLayout} {
		if t, err := time.ParseInLocation(layout, v, jst); err == nil {
			return EventTime{DateTime: t.Format(time.RFC3339)}, nil
		}
	}
	return EventTime{}, fmt.Errorf("日時 %q を解釈できません。YYYY-MM-DD(終日)またはYYYY-MM-DDTHH:MM形式で指定してください", v)
}

// normalizeDayRange はfrom/toが空の場合に「今日(JST)」で補います。
// 片方だけ指定された場合はもう片方を同じ日にします。
func normalizeDayRange(from, to string) (string, string) {
	if from != "" && to != "" {
		return from, to
	}
	today := time.Now().In(jst).Format(mfDateLayout)
	switch {
	case from == "" && to == "":
		return today, today
	case from == "":
		return to, to
	default:
		return from, from
	}
}

// CalendarClient はGoogleカレンダー連携に必要な操作を抽象化するインターフェースです。
// 予定の削除・変更は誤爆時に戻せないため、意図的に公開していません。
type CalendarClient interface {
	// ListEvents はfrom〜to(YYYY-MM-DD、JST)の予定を開始時刻の昇順で返します。
	// 空の場合は今日が対象です。繰り返し予定は個々の回に展開して返します。
	ListEvents(ctx context.Context, from, to string, limit int) ([]CalendarEvent, error)
	// CreateEvent は自分のカレンダーに予定を作成します。他の参加者は追加しません。
	CreateEvent(ctx context.Context, title string, start, end EventTime, description, location string) (CalendarEvent, error)
}

// MockCalendarClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockCalendarClient struct{}

func NewMockCalendarClient() *MockCalendarClient { return &MockCalendarClient{} }

func (m *MockCalendarClient) ListEvents(ctx context.Context, from, to string, limit int) ([]CalendarEvent, error) {
	log.Printf("[MockCalendarClient] ListEvents(from=%s, to=%s) called (Google認証情報が未設定のためモック応答)", from, to)
	from, _ = normalizeDayRange(from, to)
	return []CalendarEvent{{
		ID:       "mock-event-1",
		Title:    "[サンプル] 定例ミーティング",
		Start:    from + "T10:00:00+09:00",
		End:      from + "T11:00:00+09:00",
		Location: "オンライン",
		URL:      "https://calendar.google.com/calendar/event?eid=mock",
	}}, nil
}

func (m *MockCalendarClient) CreateEvent(ctx context.Context, title string, start, end EventTime, description, location string) (CalendarEvent, error) {
	log.Printf("[MockCalendarClient] CreateEvent(title=%s, start=%+v) called (Google認証情報が未設定のためモック応答)", title, start)
	return CalendarEvent{
		ID:     "mock-created-event-1",
		Title:  title,
		Start:  firstNonEmpty(start.DateTime, start.Date),
		End:    firstNonEmpty(end.DateTime, end.Date),
		AllDay: start.IsAllDay(),
		URL:    "https://calendar.google.com/calendar/event?eid=mock-created",
	}, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewCalendarTools はClaudeのFunction CallingからCalendarClientを操作するためのBetaToolセットを構築します。
func NewCalendarTools(client CalendarClient) ([]anthropic.BetaTool, error) {
	listTool, err := toolrunner.NewBetaToolFromBytes[calendarListInput](
		"calendar_list_events",
		"Googleカレンダーの予定を期間で取得する。「今日の予定」「明日の予定」「今週の予定」等に使う。"+
			"期間を省略すると今日(JST)が対象。繰り返し予定は個々の回に展開して返る。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from":  map[string]any{"type": "string", "description": "期間の開始日(YYYY-MM-DD)。省略時は今日"},
				"to":    map[string]any{"type": "string", "description": "期間の終了日(YYYY-MM-DD、その日を含む)。省略時はfromと同じ日"},
				"limit": map[string]any{"type": "integer", "description": "最大件数(既定20、最大100)"},
			},
		}),
		func(ctx context.Context, in calendarListInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			events, err := client.ListEvents(ctx, in.From, in.To, in.Limit)
			if err != nil {
				return textResult(""), err
			}
			from, to := normalizeDayRange(in.From, in.To)
			return textResult(formatCalendarEvents(from, to, events)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	createTool, err := toolrunner.NewBetaToolFromBytes[calendarCreateInput](
		"calendar_create_event",
		"Googleカレンダーに自分の予定を作成する。作業時間の確保や打ち合わせの仮押さえに使う。"+
			"他の参加者を招待する機能はないため、参加者を招く必要がある場合は予定を作ったうえでその旨を伝えること。"+
			"日時はJSTで解釈される。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string", "description": "予定のタイトル"},
				"start":       map[string]any{"type": "string", "description": "開始日時。YYYY-MM-DDTHH:MM形式。終日予定ならYYYY-MM-DD"},
				"end":         map[string]any{"type": "string", "description": "終了日時。startと同じ形式。終日予定の場合は開始日と同じ日付でよい"},
				"description": map[string]any{"type": "string", "description": "予定の詳細(任意)"},
				"location":    map[string]any{"type": "string", "description": "場所(任意)"},
			},
			"required": []string{"title", "start", "end"},
		}),
		func(ctx context.Context, in calendarCreateInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			start, err := parseEventTime(in.Start)
			if err != nil {
				return textResult(""), fmt.Errorf("start: %w", err)
			}
			end, err := parseEventTime(in.End)
			if err != nil {
				return textResult(""), fmt.Errorf("end: %w", err)
			}
			// 終日と時刻付きが混ざると意図が読めないため弾く。
			if start.IsAllDay() != end.IsAllDay() {
				return textResult(""), fmt.Errorf("startとendは両方とも終日(YYYY-MM-DD)か両方とも時刻付き(YYYY-MM-DDTHH:MM)で指定してください")
			}

			event, err := client.CreateEvent(ctx, in.Title, start, end, in.Description, in.Location)
			if err != nil {
				return textResult(""), err
			}
			b, err := json.Marshal(event)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{listTool, createTool}, nil
}

// formatCalendarEvents は予定一覧をClaudeが読みやすいテキストにまとめます。
func formatCalendarEvents(from, to string, events []CalendarEvent) string {
	var b strings.Builder
	period := from
	if from != to {
		period = from + "〜" + to
	}
	if len(events) == 0 {
		return fmt.Sprintf("予定(%s): 0件(予定はありません)", period)
	}
	fmt.Fprintf(&b, "予定(%s): %d件\n", period, len(events))
	for _, e := range events {
		if e.AllDay {
			fmt.Fprintf(&b, "- [終日] %s", e.Title)
		} else {
			fmt.Fprintf(&b, "- %s〜%s %s", shortTime(e.Start), shortTime(e.End), e.Title)
		}
		if e.Location != "" {
			fmt.Fprintf(&b, " @%s", e.Location)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// shortTime はRFC3339からHH:MMだけを取り出します。解釈できない場合はそのまま返します。
func shortTime(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.In(jst).Format("15:04")
}

type calendarListInput struct {
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type calendarCreateInput struct {
	Title       string `json:"title"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
}
