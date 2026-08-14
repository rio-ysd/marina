package tools

import (
	"context"
	"fmt"

	"golang.org/x/oauth2/google"
	googleoption "google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// sheetsScopes はGoogleスプレッドシート連携に必要なOAuthスコープです。
// 行の追記を行うため参照専用(spreadsheets.readonly)では足りません。
// 上書き・クリア・シート削除のツールは公開しておらず、操作範囲はツールセットで限定しています。
var sheetsScopes = []string{sheets.SpreadsheetsScope}

// spreadsheetInfoFields は必要なフィールドだけを指定します(既定だと全セルのデータまで返る)。
const spreadsheetInfoFields = "spreadsheetId,spreadsheetUrl,properties.title,sheets.properties(title,gridProperties(rowCount,columnCount))"

// RealSheetsClient はGoogle Workspaceサービスアカウント + ドメイン委任でSheets APIを呼び出す実装です。
// 認証情報はGmail/Drive/カレンダー連携と共通です。
type RealSheetsClient struct {
	service *sheets.Service
}

// NewRealSheetsClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからSheetsClientを構築します。
func NewRealSheetsClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealSheetsClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), sheetsScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := sheets.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create sheets service: %w", err)
	}
	return &RealSheetsClient{service: svc}, nil
}

func (c *RealSheetsClient) GetInfo(ctx context.Context, spreadsheetID string) (SpreadsheetInfo, error) {
	resp, err := c.service.Spreadsheets.Get(spreadsheetID).
		Fields(spreadsheetInfoFields).
		Context(ctx).
		Do()
	if err != nil {
		return SpreadsheetInfo{}, fmt.Errorf("get spreadsheet %s: %w", spreadsheetID, err)
	}

	info := SpreadsheetInfo{ID: resp.SpreadsheetId, URL: resp.SpreadsheetUrl}
	if resp.Properties != nil {
		info.Title = resp.Properties.Title
	}
	for _, sheet := range resp.Sheets {
		if sheet.Properties == nil {
			continue
		}
		tab := SheetTab{Title: sheet.Properties.Title}
		if grid := sheet.Properties.GridProperties; grid != nil {
			tab.RowCount = grid.RowCount
			tab.ColumnCount = grid.ColumnCount
		}
		info.Tabs = append(info.Tabs, tab)
	}
	return info, nil
}

func (c *RealSheetsClient) ReadRange(ctx context.Context, spreadsheetID, a1Range string, maxRows int) (SheetValues, error) {
	resp, err := c.service.Spreadsheets.Values.Get(spreadsheetID, a1Range).
		Context(ctx).
		Do()
	if err != nil {
		return SheetValues{}, fmt.Errorf("read range %s of %s: %w", a1Range, spreadsheetID, err)
	}

	limit := normalizeSheetRows(maxRows)
	truncated := len(resp.Values) > limit
	rawRows := resp.Values
	if truncated {
		rawRows = rawRows[:limit]
	}

	rows := make([][]string, 0, len(rawRows))
	for _, rawRow := range rawRows {
		row := make([]string, 0, len(rawRow))
		for _, cell := range rawRow {
			row = append(row, truncateCell(cellToString(cell)))
		}
		rows = append(rows, row)
	}

	return SheetValues{Range: resp.Range, Rows: rows, Truncated: truncated}, nil
}

func (c *RealSheetsClient) AppendRows(ctx context.Context, spreadsheetID, a1Range string, rows [][]string) (int, error) {
	values := make([][]any, 0, len(rows))
	for _, row := range rows {
		cells := make([]any, 0, len(row))
		for _, cell := range row {
			cells = append(cells, cell)
		}
		values = append(values, cells)
	}

	resp, err := c.service.Spreadsheets.Values.
		Append(spreadsheetID, a1Range, &sheets.ValueRange{Values: values}).
		// USER_ENTERED は入力欄に打ち込んだのと同じ扱い(日付や数値として解釈され、数式も有効になる)。
		ValueInputOption("USER_ENTERED").
		// INSERT_ROWS は行を挿入して追記する。OVERWRITEだと既存行を壊すため使わない。
		InsertDataOption("INSERT_ROWS").
		Context(ctx).
		Do()
	if err != nil {
		return 0, fmt.Errorf("append rows to %s of %s: %w", a1Range, spreadsheetID, err)
	}

	if resp.Updates != nil {
		return int(resp.Updates.UpdatedRows), nil
	}
	return len(rows), nil
}

// cellToString はAPIが返すセル値(文字列・数値・真偽値)を文字列にします。
// 表示用の値(FORMATTED_VALUE)が返るため通常は文字列ですが、型が揺れても落ちないようにします。
func cellToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// 整数なら小数点以下を付けない(1.0ではなく1)。
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
