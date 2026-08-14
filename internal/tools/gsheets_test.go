package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	googleoption "google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// newTestSheetsClient はテスト用HTTPサーバーを向いた実クライアントを構築します。
func newTestSheetsClient(t *testing.T, handler http.HandlerFunc) *RealSheetsClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := sheets.NewService(context.Background(),
		googleoption.WithHTTPClient(srv.Client()),
		googleoption.WithEndpoint(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("sheets.NewService() error: %v", err)
	}
	return &RealSheetsClient{service: svc}
}

func TestGetInfoReturnsSheetTabs(t *testing.T) {
	var gotQuery url.Values
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"spreadsheetId":"sid","spreadsheetUrl":"https://docs.google.com/spreadsheets/d/sid/edit",
			"properties":{"title":"売上管理"},
			"sheets":[
				{"properties":{"title":"売上","gridProperties":{"rowCount":1000,"columnCount":26}}},
				{"properties":{"title":"経費"}}
			]}`))
	})

	info, err := client.GetInfo(context.Background(), "sid")
	if err != nil {
		t.Fatalf("GetInfo() error: %v", err)
	}

	// fieldsを絞らないと全セルのデータまで返ってくる。
	if got := gotQuery.Get("fields"); !strings.Contains(got, "sheets.properties") {
		t.Errorf("fields = %q, want it to limit the response", got)
	}
	if info.Title != "売上管理" || info.URL != "https://docs.google.com/spreadsheets/d/sid/edit" {
		t.Errorf("info mismatch: %+v", info)
	}
	if len(info.Tabs) != 2 {
		t.Fatalf("Tabs len = %d, want 2", len(info.Tabs))
	}
	if info.Tabs[0].Title != "売上" || info.Tabs[0].RowCount != 1000 || info.Tabs[0].ColumnCount != 26 {
		t.Errorf("Tabs[0] = %+v", info.Tabs[0])
	}
	// gridPropertiesが無いシートでも落ちないこと。
	if info.Tabs[1].Title != "経費" || info.Tabs[1].RowCount != 0 {
		t.Errorf("Tabs[1] = %+v", info.Tabs[1])
	}
}

func TestReadRangeParsesRows(t *testing.T) {
	var gotPath string
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"range":"売上!A1:C3","values":[
			["日付","取引先","金額"],
			["2026-08-01","牧野電設工業株式会社",49500],
			["2026-08-02","A社",110000.5]
		]}`))
	})

	got, err := client.ReadRange(context.Background(), "sid", "売上!A1:C3", 0)
	if err != nil {
		t.Fatalf("ReadRange() error: %v", err)
	}

	if !strings.Contains(gotPath, "/values/") {
		t.Errorf("path = %q, want it to hit the values endpoint", gotPath)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
	if len(got.Rows) != 3 {
		t.Fatalf("Rows len = %d, want 3", len(got.Rows))
	}
	// 数値セルは文字列化する。整数に .0 が付かないこと。
	if got.Rows[1][2] != "49500" {
		t.Errorf("整数セル = %q, want 49500", got.Rows[1][2])
	}
	if got.Rows[2][2] != "110000.5" {
		t.Errorf("小数セル = %q, want 110000.5", got.Rows[2][2])
	}
}

func TestReadRangeTruncatesRows(t *testing.T) {
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`{"range":"売上","values":[`)
		for i := 0; i < 10; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`["row"]`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	})

	got, err := client.ReadRange(context.Background(), "sid", "売上", 3)
	if err != nil {
		t.Fatalf("ReadRange() error: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(got.Rows) != 3 {
		t.Errorf("Rows len = %d, want 3", len(got.Rows))
	}
	if text := formatSheetValues(got); !strings.Contains(text, "先頭3行のみ") {
		t.Errorf("打ち切りの注記がない: %s", text)
	}
}

// 長文メモの列で入力が膨らまないようセル単位でも打ち切る。
func TestReadRangeTruncatesLongCell(t *testing.T) {
	long := strings.Repeat("あ", maxSheetCellChars+50)
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"range":"メモ","values":[["` + long + `"]]}`))
	})

	got, err := client.ReadRange(context.Background(), "sid", "メモ", 0)
	if err != nil {
		t.Fatalf("ReadRange() error: %v", err)
	}
	cell := got.Rows[0][0]
	if runes := []rune(cell); len(runes) != maxSheetCellChars+1 {
		t.Errorf("セルの文字数 = %d, want %d (打ち切り + 省略記号)", len(runes), maxSheetCellChars+1)
	}
	if !strings.HasSuffix(cell, "…") {
		t.Errorf("省略記号が付いていない: %q", cell[len(cell)-10:])
	}
}

func TestAppendRowsInsertsWithoutOverwriting(t *testing.T) {
	var gotBody string
	var gotQuery url.Values
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"updates":{"updatedRows":2}}`))
	})

	appended, err := client.AppendRows(context.Background(), "sid", "売上", [][]string{
		{"2026-08-13", "A社", "110000"},
		{"2026-08-13", "B社", "55000"},
	})
	if err != nil {
		t.Fatalf("AppendRows() error: %v", err)
	}

	if got := gotQuery.Get("valueInputOption"); got != "USER_ENTERED" {
		t.Errorf("valueInputOption = %q, want USER_ENTERED", got)
	}
	// OVERWRITEだと既存行を壊すため、必ずINSERT_ROWSで追記する。
	if got := gotQuery.Get("insertDataOption"); got != "INSERT_ROWS" {
		t.Errorf("insertDataOption = %q, want INSERT_ROWS", got)
	}
	if !strings.Contains(gotBody, "110000") || !strings.Contains(gotBody, "B社") {
		t.Errorf("body に行データがない: %s", gotBody)
	}
	if appended != 2 {
		t.Errorf("appended = %d, want 2", appended)
	}
}

// updatedRowsが返らない実装差があっても、送った行数を返して黙って0にしない。
func TestAppendRowsFallsBackToSentRowCount(t *testing.T) {
	client := newTestSheetsClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	appended, err := client.AppendRows(context.Background(), "sid", "売上", [][]string{{"a"}})
	if err != nil {
		t.Fatalf("AppendRows() error: %v", err)
	}
	if appended != 1 {
		t.Errorf("appended = %d, want 1", appended)
	}
}

func TestNormalizeSheetRows(t *testing.T) {
	for in, want := range map[int]int{0: 50, -3: 50, 10: 10, 500: 500, 5000: 500} {
		if got := normalizeSheetRows(in); got != want {
			t.Errorf("normalizeSheetRows(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCellToString(t *testing.T) {
	for _, tt := range []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"text", "text"},
		{float64(49500), "49500"},
		{110000.5, "110000.5"},
		{true, "true"},
	} {
		if got := cellToString(tt.in); got != tt.want {
			t.Errorf("cellToString(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// 何行目の話かが分かるよう行番号を添える。
func TestFormatSheetValuesNumbersRows(t *testing.T) {
	got := formatSheetValues(SheetValues{
		Range: "売上!A1:C2",
		Rows:  [][]string{{"日付", "取引先", "金額"}, {"2026-08-01", "A社", "110000"}},
	})

	if !strings.Contains(got, "売上!A1:C2: 2行") {
		t.Errorf("範囲/行数が出ていない:\n%s", got)
	}
	if !strings.Contains(got, "1: 日付 | 取引先 | 金額") {
		t.Errorf("見出し行の整形がおかしい:\n%s", got)
	}
	if !strings.Contains(got, "2: 2026-08-01 | A社 | 110000") {
		t.Errorf("データ行の整形がおかしい:\n%s", got)
	}
}

func TestFormatSheetValuesEmpty(t *testing.T) {
	if got := formatSheetValues(SheetValues{Range: "売上"}); !strings.Contains(got, "空") {
		t.Errorf("空であることが伝わらない: %s", got)
	}
}

func TestNewSheetsToolsDefinesExpectedTools(t *testing.T) {
	sheetsTools, err := NewSheetsTools(NewMockSheetsClient())
	if err != nil {
		t.Fatalf("NewSheetsTools() error: %v", err)
	}

	got := make(map[string]bool, len(sheetsTools))
	for _, tool := range sheetsTools {
		got[tool.Name()] = true
	}
	for _, name := range []string{"sheets_get_info", "sheets_read_range", "sheets_append_rows"} {
		if !got[name] {
			t.Errorf("ツール %s が定義されていない (定義済み: %v)", name, got)
		}
	}
	// 上書き・クリア・削除は失ったデータに気づけないため公開しない。
	for _, name := range []string{"sheets_update_range", "sheets_clear_range", "sheets_delete_sheet"} {
		if got[name] {
			t.Errorf("ツール %s は公開しない方針", name)
		}
	}
	if len(sheetsTools) != 3 {
		t.Errorf("ツール数 = %d, want 3", len(sheetsTools))
	}
}
