package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"
	googleoption "google.golang.org/api/option"
)

// newTestCalendarClient はテスト用HTTPサーバーを向いた実クライアントを構築します。
func newTestCalendarClient(t *testing.T, handler http.HandlerFunc) *RealCalendarClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := calendar.NewService(context.Background(),
		googleoption.WithHTTPClient(srv.Client()),
		googleoption.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("calendar.NewService() error: %v", err)
	}
	return &RealCalendarClient{service: svc, calendarID: primaryCalendarID}
}

func TestParseEventTime(t *testing.T) {
	tests := []struct {
		in       string
		wantDate string
		wantTime string
	}{
		{in: "2026-08-14", wantDate: "2026-08-14"},
		{in: "2026-08-14T10:00", wantTime: "2026-08-14T10:00:00+09:00"},
		{in: "2026-08-14 10:00", wantTime: "2026-08-14T10:00:00+09:00"},
		{in: "2026-08-14T10:00:30", wantTime: "2026-08-14T10:00:30+09:00"},
		// タイムゾーン付きで来た場合はJSTに直して保持する。
		{in: "2026-08-14T01:00:00Z", wantTime: "2026-08-14T10:00:00+09:00"},
		{in: " 2026-08-14T10:00 ", wantTime: "2026-08-14T10:00:00+09:00"},
	}
	for _, tt := range tests {
		got, err := parseEventTime(tt.in)
		if err != nil {
			t.Errorf("parseEventTime(%q) error: %v", tt.in, err)
			continue
		}
		if got.Date != tt.wantDate || got.DateTime != tt.wantTime {
			t.Errorf("parseEventTime(%q) = %+v, want Date=%q DateTime=%q", tt.in, got, tt.wantDate, tt.wantTime)
		}
		if got.IsAllDay() != (tt.wantDate != "") {
			t.Errorf("parseEventTime(%q).IsAllDay() = %v", tt.in, got.IsAllDay())
		}
	}
}

func TestParseEventTimeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "  ", "明日の10時", "2026/08/14", "10:00"} {
		if _, err := parseEventTime(in); err == nil {
			t.Errorf("parseEventTime(%q) error = nil, want error", in)
		}
	}
}

func TestNormalizeDayRange(t *testing.T) {
	today := time.Now().In(jst).Format(mfDateLayout)

	if from, to := normalizeDayRange("", ""); from != today || to != today {
		t.Errorf("normalizeDayRange(\"\",\"\") = %q,%q, want %q,%q (既定は今日)", from, to, today, today)
	}
	if from, to := normalizeDayRange("2026-08-01", "2026-08-31"); from != "2026-08-01" || to != "2026-08-31" {
		t.Errorf("両方指定が保持されない: %q,%q", from, to)
	}
	// 片方だけ来たら同じ日として扱う(その日1日の予定を聞かれたケース)。
	if from, to := normalizeDayRange("2026-08-14", ""); from != "2026-08-14" || to != "2026-08-14" {
		t.Errorf("normalizeDayRange(fromのみ) = %q,%q", from, to)
	}
	if from, to := normalizeDayRange("", "2026-08-14"); from != "2026-08-14" || to != "2026-08-14" {
		t.Errorf("normalizeDayRange(toのみ) = %q,%q", from, to)
	}
}

func TestListEventsSendsRangeAndExpandsRecurring(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client := newTestCalendarClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":"e1","summary":"定例MTG","location":"オンライン","htmlLink":"https://calendar.google.com/e1",
			 "start":{"dateTime":"2026-08-14T10:00:00+09:00"},"end":{"dateTime":"2026-08-14T11:00:00+09:00"}},
			{"id":"e2","summary":"終日イベント","start":{"date":"2026-08-14"},"end":{"date":"2026-08-15"}}
		]}`))
	})

	events, err := client.ListEvents(context.Background(), "2026-08-14", "2026-08-14", 5)
	if err != nil {
		t.Fatalf("ListEvents() error: %v", err)
	}

	if !strings.HasSuffix(gotPath, "/calendars/primary/events") {
		t.Errorf("path = %q, want it to end with /calendars/primary/events", gotPath)
	}
	// 繰り返し予定を展開しないと「今日の予定」に出てこない。
	if got := gotQuery.Get("singleEvents"); got != "true" {
		t.Errorf("singleEvents = %q, want true", got)
	}
	if got := gotQuery.Get("orderBy"); got != "startTime" {
		t.Errorf("orderBy = %q, want startTime", got)
	}
	if got := gotQuery.Get("maxResults"); got != "5" {
		t.Errorf("maxResults = %q, want 5", got)
	}
	if got := gotQuery.Get("timeMin"); got != "2026-08-14T00:00:00+09:00" {
		t.Errorf("timeMin = %q, want 2026-08-14T00:00:00+09:00 (JSTの0時)", got)
	}
	// toの当日を含めるため上限は翌日0時。23:59:59だと終日予定を取りこぼす。
	if got := gotQuery.Get("timeMax"); got != "2026-08-15T00:00:00+09:00" {
		t.Errorf("timeMax = %q, want 2026-08-15T00:00:00+09:00 (翌日0時)", got)
	}

	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Title != "定例MTG" || events[0].AllDay || events[0].Location != "オンライン" {
		t.Errorf("時刻付き予定の変換がおかしい: %+v", events[0])
	}
	if !events[1].AllDay || events[1].Start != "2026-08-14" {
		t.Errorf("終日予定の変換がおかしい: %+v", events[1])
	}
}

func TestListEventsDefaultsToToday(t *testing.T) {
	var gotQuery url.Values
	client := newTestCalendarClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	if _, err := client.ListEvents(context.Background(), "", "", 0); err != nil {
		t.Fatalf("ListEvents() error: %v", err)
	}

	today := time.Now().In(jst)
	wantMin := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, jst).Format(time.RFC3339)
	if got := gotQuery.Get("timeMin"); got != wantMin {
		t.Errorf("timeMin = %q, want %q (今日の0時)", got, wantMin)
	}
	if got := gotQuery.Get("maxResults"); got != "20" {
		t.Errorf("maxResults = %q, want 20 (limit未指定時の既定値)", got)
	}
}

func TestListEventsRejectsBadDate(t *testing.T) {
	client := newTestCalendarClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("不正な日付でAPIを呼んではいけない")
	})

	if _, err := client.ListEvents(context.Background(), "2026/08/14", "2026/08/14", 0); err == nil {
		t.Fatal("ListEvents() error = nil, want error")
	}
}

func TestCreateEventSendsTimeZoneAndNoNotification(t *testing.T) {
	var gotBody, gotPath string
	var gotQuery url.Values
	client := newTestCalendarClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"created1","summary":"作業時間","htmlLink":"https://calendar.google.com/created1",
			"start":{"dateTime":"2026-08-14T10:00:00+09:00"},"end":{"dateTime":"2026-08-14T12:00:00+09:00"}}`))
	})

	start, _ := parseEventTime("2026-08-14T10:00")
	end, _ := parseEventTime("2026-08-14T12:00")
	event, err := client.CreateEvent(context.Background(), "作業時間", start, end, "集中作業", "自席")
	if err != nil {
		t.Fatalf("CreateEvent() error: %v", err)
	}

	if !strings.HasSuffix(gotPath, "/calendars/primary/events") {
		t.Errorf("path = %q", gotPath)
	}
	// 参加者を追加しないので招待メールも飛ばさない。
	if got := gotQuery.Get("sendUpdates"); got != "none" {
		t.Errorf("sendUpdates = %q, want none", got)
	}
	// TZを付けないとLambda(UTC)で9時間ずれる。
	if !strings.Contains(gotBody, calendarTimeZone) {
		t.Errorf("body に timeZone がない: %s", gotBody)
	}
	if !strings.Contains(gotBody, "2026-08-14T10:00:00+09:00") {
		t.Errorf("body に開始日時がない: %s", gotBody)
	}
	if !strings.Contains(gotBody, "集中作業") || !strings.Contains(gotBody, "自席") {
		t.Errorf("body に詳細/場所がない: %s", gotBody)
	}

	if event.ID != "created1" || event.URL != "https://calendar.google.com/created1" {
		t.Errorf("created event mismatch: %+v", event)
	}
}

// 終日予定のendは排他的なので、指定日を含めるには翌日を送る必要がある。
func TestCreateEventAllDayShiftsEndDate(t *testing.T) {
	var gotBody string
	client := newTestCalendarClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"allday1","summary":"有給","start":{"date":"2026-08-14"},"end":{"date":"2026-08-15"}}`))
	})

	start, _ := parseEventTime("2026-08-14")
	end, _ := parseEventTime("2026-08-14")
	if _, err := client.CreateEvent(context.Background(), "有給", start, end, "", ""); err != nil {
		t.Fatalf("CreateEvent() error: %v", err)
	}

	var sent calendar.Event
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("送信ボディをパースできない: %v / %s", err, gotBody)
	}
	if sent.Start == nil || sent.Start.Date != "2026-08-14" {
		t.Errorf("start.date = %+v, want 2026-08-14", sent.Start)
	}
	if sent.End == nil || sent.End.Date != "2026-08-15" {
		t.Errorf("end.date = %+v, want 2026-08-15 (指定日を含めるため+1日)", sent.End)
	}
	// 終日予定にdateTimeを混ぜると400になる。
	if sent.Start.DateTime != "" || sent.End.DateTime != "" {
		t.Errorf("終日予定にdateTimeが入っている: %s", gotBody)
	}
}

func TestFormatCalendarEvents(t *testing.T) {
	got := formatCalendarEvents("2026-08-14", "2026-08-14", []CalendarEvent{
		{Title: "定例MTG", Start: "2026-08-14T10:00:00+09:00", End: "2026-08-14T11:00:00+09:00", Location: "オンライン"},
		{Title: "有給", Start: "2026-08-14", AllDay: true},
	})

	if !strings.Contains(got, "予定(2026-08-14): 2件") {
		t.Errorf("件数が出ていない:\n%s", got)
	}
	if !strings.Contains(got, "10:00〜11:00 定例MTG @オンライン") {
		t.Errorf("時刻付き予定の整形がおかしい:\n%s", got)
	}
	if !strings.Contains(got, "[終日] 有給") {
		t.Errorf("終日予定の整形がおかしい:\n%s", got)
	}

	// 期間指定のときは範囲を出す。
	ranged := formatCalendarEvents("2026-08-14", "2026-08-20", nil)
	if !strings.Contains(ranged, "2026-08-14〜2026-08-20") || !strings.Contains(ranged, "0件") {
		t.Errorf("期間/0件の表示がおかしい: %s", ranged)
	}
}

func TestNewCalendarToolsDefinesExpectedTools(t *testing.T) {
	calendarTools, err := NewCalendarTools(NewMockCalendarClient())
	if err != nil {
		t.Fatalf("NewCalendarTools() error: %v", err)
	}

	got := make(map[string]bool, len(calendarTools))
	for _, tool := range calendarTools {
		got[tool.Name()] = true
	}
	for _, name := range []string{"calendar_list_events", "calendar_create_event"} {
		if !got[name] {
			t.Errorf("ツール %s が定義されていない (定義済み: %v)", name, got)
		}
	}
	// 予定の削除・変更は誤爆時に戻せないため公開しない。
	for _, name := range []string{"calendar_delete_event", "calendar_update_event"} {
		if got[name] {
			t.Errorf("ツール %s は公開しない方針", name)
		}
	}
	if len(calendarTools) != 2 {
		t.Errorf("ツール数 = %d, want 2", len(calendarTools))
	}
}
