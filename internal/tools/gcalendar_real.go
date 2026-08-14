package tools

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	googleoption "google.golang.org/api/option"
)

// calendarScopes はGoogleカレンダー連携に必要なOAuthスコープです。
// 予定の作成を行うため参照専用(calendar.readonly)では足りません。
// 削除・変更のツールは公開しておらず、操作範囲はツールセットで限定しています。
var calendarScopes = []string{calendar.CalendarScope}

const (
	// primaryCalendarID は代理対象ユーザーのメインカレンダーです。
	primaryCalendarID = "primary"
	// calendarTimeZone は作成する予定のタイムゾーン。LambdaのTZはUTCなので明示します。
	calendarTimeZone = "Asia/Tokyo"
)

// RealCalendarClient はGoogle Workspaceサービスアカウント + ドメイン委任でCalendar APIを呼び出す実装です。
// 認証情報はGmail/Drive連携と共通です。
type RealCalendarClient struct {
	service    *calendar.Service
	calendarID string
}

// NewRealCalendarClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからCalendarClientを構築します。
func NewRealCalendarClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealCalendarClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), calendarScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := calendar.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create calendar service: %w", err)
	}
	return &RealCalendarClient{service: svc, calendarID: primaryCalendarID}, nil
}

func (c *RealCalendarClient) ListEvents(ctx context.Context, from, to string, limit int) ([]CalendarEvent, error) {
	from, to = normalizeDayRange(from, to)

	timeMin, err := dayStart(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	// toはその日を含めるため、翌日の0時を上限にします(23:59:59だと終日予定を取りこぼす)。
	timeMax, err := dayStart(to)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	timeMax = timeMax.AddDate(0, 0, 1)

	resp, err := c.service.Events.List(c.calendarID).
		TimeMin(timeMin.Format(time.RFC3339)).
		TimeMax(timeMax.Format(time.RFC3339)).
		// 繰り返し予定を個々の回に展開する。展開しないと「今日の予定」に出てこない。
		SingleEvents(true).
		OrderBy("startTime").
		MaxResults(int64(normalizeLimit(limit))).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("list events (%s〜%s): %w", from, to, err)
	}

	events := make([]CalendarEvent, 0, len(resp.Items))
	for _, item := range resp.Items {
		events = append(events, toCalendarEvent(item))
	}
	return events, nil
}

func (c *RealCalendarClient) CreateEvent(ctx context.Context, title string, start, end EventTime, description, location string) (CalendarEvent, error) {
	event := &calendar.Event{
		Summary:     title,
		Description: description,
		Location:    location,
		Start:       toEventDateTime(start),
		End:         toEventDateTime(end),
	}
	// 終日予定のendは排他的(その日を含めるには翌日を指定する)ため、指定日を含むように1日足します。
	if end.IsAllDay() {
		endDate, err := time.ParseInLocation(mfDateLayout, end.Date, jst)
		if err != nil {
			return CalendarEvent{}, fmt.Errorf("end: %w", err)
		}
		event.End = &calendar.EventDateTime{Date: endDate.AddDate(0, 0, 1).Format(mfDateLayout)}
	}

	created, err := c.service.Events.Insert(c.calendarID, event).
		// 参加者を追加しないので通知も送らない。招待メールが意図せず飛ぶのを防ぐ。
		SendUpdates("none").
		Context(ctx).
		Do()
	if err != nil {
		return CalendarEvent{}, fmt.Errorf("create event %q: %w", title, err)
	}
	return toCalendarEvent(created), nil
}

// dayStart はYYYY-MM-DDをJSTのその日の0時に変換します。
func dayStart(date string) (time.Time, error) {
	t, err := time.ParseInLocation(mfDateLayout, date, jst)
	if err != nil {
		return time.Time{}, fmt.Errorf("日付 %q はYYYY-MM-DD形式で指定してください", date)
	}
	return t, nil
}

func toEventDateTime(t EventTime) *calendar.EventDateTime {
	if t.IsAllDay() {
		return &calendar.EventDateTime{Date: t.Date}
	}
	return &calendar.EventDateTime{DateTime: t.DateTime, TimeZone: calendarTimeZone}
}

func toCalendarEvent(item *calendar.Event) CalendarEvent {
	e := CalendarEvent{
		ID:       item.Id,
		Title:    item.Summary,
		Location: item.Location,
		URL:      item.HtmlLink,
	}
	if item.Start != nil {
		e.Start = firstNonEmpty(item.Start.DateTime, item.Start.Date)
		e.AllDay = item.Start.DateTime == "" && item.Start.Date != ""
	}
	if item.End != nil {
		e.End = firstNonEmpty(item.End.DateTime, item.End.Date)
	}
	return e
}
