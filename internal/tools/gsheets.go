package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// SheetTab はスプレッドシート内の1シート(タブ)です。
type SheetTab struct {
	Title string `json:"title"`
	// RowCount/ColumnCount はグリッドのサイズ(実際に値が入っている範囲ではない)。
	RowCount    int64 `json:"row_count,omitempty"`
	ColumnCount int64 `json:"column_count,omitempty"`
}

// SpreadsheetInfo はスプレッドシートの基本情報です。
// 範囲を指定して読む前に、どんなシートがあるかを知るために使います。
type SpreadsheetInfo struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	URL   string     `json:"url,omitempty"`
	Tabs  []SheetTab `json:"tabs"`
}

// SheetValues は範囲読み取りの結果です。
// Truncatedがtrueの場合、Rowsはmax_rowsで打ち切られています。
type SheetValues struct {
	Range     string     `json:"range"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// 読み取り行数の既定値と上限。セルの内容がそのままClaudeの入力になるため上限を持たせます。
const (
	defaultSheetRows = 50
	maxSheetRows     = 500
	// maxSheetCellChars は1セルあたりの文字数上限。長文メモが入った列で入力が膨らむのを防ぎます。
	maxSheetCellChars = 200
)

func normalizeSheetRows(rows int) int {
	switch {
	case rows <= 0:
		return defaultSheetRows
	case rows > maxSheetRows:
		return maxSheetRows
	default:
		return rows
	}
}

// SheetsClient はGoogleスプレッドシート連携に必要な操作を抽象化するインターフェースです。
// 既存セルの上書き・クリア・シート削除は失ったデータに気づけないため、意図的に公開していません。
type SheetsClient interface {
	// GetInfo はスプレッドシートのタイトルとシート(タブ)の一覧を返します。
	GetInfo(ctx context.Context, spreadsheetID string) (SpreadsheetInfo, error)
	// ReadRange はA1記法の範囲(例: "売上!A1:D20")を読み取ります。
	// 範囲が空ならシート全体が対象で、maxRows行で打ち切ります。
	ReadRange(ctx context.Context, spreadsheetID, a1Range string, maxRows int) (SheetValues, error)
	// AppendRows は指定シートの末尾に行を追記します。既存の行は変更しません。
	AppendRows(ctx context.Context, spreadsheetID, a1Range string, rows [][]string) (int, error)
}

// MockSheetsClient はGoogle認証情報が未設定の間に使うスタブ実装です。
type MockSheetsClient struct{}

func NewMockSheetsClient() *MockSheetsClient { return &MockSheetsClient{} }

func (m *MockSheetsClient) GetInfo(ctx context.Context, spreadsheetID string) (SpreadsheetInfo, error) {
	log.Printf("[MockSheetsClient] GetInfo(%s) called (Google認証情報が未設定のためモック応答)", spreadsheetID)
	return SpreadsheetInfo{
		ID:    spreadsheetID,
		Title: "[サンプル] 売上管理",
		URL:   "https://docs.google.com/spreadsheets/d/" + spreadsheetID + "/edit",
		Tabs:  []SheetTab{{Title: "売上", RowCount: 1000, ColumnCount: 26}},
	}, nil
}

func (m *MockSheetsClient) ReadRange(ctx context.Context, spreadsheetID, a1Range string, maxRows int) (SheetValues, error) {
	log.Printf("[MockSheetsClient] ReadRange(%s, %s) called (Google認証情報が未設定のためモック応答)", spreadsheetID, a1Range)
	return SheetValues{
		Range: a1Range,
		Rows: [][]string{
			{"日付", "取引先", "金額"},
			{"2026-08-01", "モック株式会社", "110000"},
		},
	}, nil
}

func (m *MockSheetsClient) AppendRows(ctx context.Context, spreadsheetID, a1Range string, rows [][]string) (int, error) {
	log.Printf("[MockSheetsClient] AppendRows(%s, %s, %d行) called (Google認証情報が未設定のためモック応答)", spreadsheetID, a1Range, len(rows))
	return len(rows), nil
}

// NewSheetsTools はClaudeのFunction CallingからSheetsClientを操作するためのBetaToolセットを構築します。
func NewSheetsTools(client SheetsClient) ([]anthropic.BetaTool, error) {
	infoTool, err := toolrunner.NewBetaToolFromBytes[sheetsInfoInput](
		"sheets_get_info",
		"Googleスプレッドシートのタイトルとシート(タブ)の一覧を取得する。"+
			"範囲を指定して読み書きする前に、どのシートがあるかを確認するために使う。"+
			"spreadsheet_idはdrive_search_files(file_type=spreadsheet)で取得しておくこと。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spreadsheet_id": map[string]any{"type": "string", "description": "スプレッドシートのID"},
			},
			"required": []string{"spreadsheet_id"},
		}),
		func(ctx context.Context, in sheetsInfoInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			info, err := client.GetInfo(ctx, in.SpreadsheetID)
			if err != nil {
				return textResult(""), err
			}
			b, err := json.Marshal(info)
			if err != nil {
				return textResult(""), err
			}
			return textResult(string(b)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	readTool, err := toolrunner.NewBetaToolFromBytes[sheetsReadInput](
		"sheets_read_range",
		"Googleスプレッドシートの範囲を読み取る。A1記法で範囲を指定する(例: 「売上!A1:D20」)。"+
			"rangeにシート名だけを渡すとそのシート全体が対象になる。"+
			"シート名が分からない場合は先にsheets_get_infoで確認すること。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spreadsheet_id": map[string]any{"type": "string", "description": "スプレッドシートのID"},
				"range":          map[string]any{"type": "string", "description": "A1記法の範囲。例: 売上!A1:D20 / 売上(シート全体)"},
				"max_rows":       map[string]any{"type": "integer", "description": "取得する最大行数(既定50、最大500)。超過分は打ち切られる"},
			},
			"required": []string{"spreadsheet_id", "range"},
		}),
		func(ctx context.Context, in sheetsReadInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			values, err := client.ReadRange(ctx, in.SpreadsheetID, in.Range, in.MaxRows)
			if err != nil {
				return textResult(""), err
			}
			return textResult(formatSheetValues(values)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	appendTool, err := toolrunner.NewBetaToolFromBytes[sheetsAppendInput](
		"sheets_append_rows",
		"Googleスプレッドシートのシート末尾に行を追記する。既存の行は変更しない。"+
			"記録の追加(実績や経費の追記など)に使う。既存セルの書き換えはできない。"+
			"列の順番を間違えないよう、先にsheets_read_rangeで見出し行を確認すること。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spreadsheet_id": map[string]any{"type": "string", "description": "スプレッドシートのID"},
				"range":          map[string]any{"type": "string", "description": "追記先のシート名(例: 売上)。A1記法の範囲でもよい"},
				"rows": map[string]any{
					"type":        "array",
					"description": "追記する行。各行は文字列の配列(列の順番どおり)",
					"items": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
			"required": []string{"spreadsheet_id", "range", "rows"},
		}),
		func(ctx context.Context, in sheetsAppendInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			if len(in.Rows) == 0 {
				return textResult(""), fmt.Errorf("追記する行がありません")
			}
			appended, err := client.AppendRows(ctx, in.SpreadsheetID, in.Range, in.Rows)
			if err != nil {
				return textResult(""), err
			}
			return textResult(fmt.Sprintf("%sに%d行を追記しました。", in.Range, appended)), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return []anthropic.BetaTool{infoTool, readTool, appendTool}, nil
}

// formatSheetValues は表をClaudeが読みやすいテキストにまとめます。
// 行番号を添えて、どの行の話をしているかが分かるようにします。
func formatSheetValues(v SheetValues) string {
	if len(v.Rows) == 0 {
		return fmt.Sprintf("%s: 空(値が入っているセルはありません)", v.Range)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d行\n", v.Range, len(v.Rows))
	if v.Truncated {
		fmt.Fprintf(&b, "(先頭%d行のみ。続きが必要ならmax_rowsを増やすか範囲を絞って再取得)\n", len(v.Rows))
	}
	for i, row := range v.Rows {
		fmt.Fprintf(&b, "%d: %s\n", i+1, strings.Join(row, " | "))
	}
	return b.String()
}

// truncateCell は1セルの文字数を制限します。長文メモの列で入力が膨らむのを防ぎます。
func truncateCell(v string) string {
	runes := []rune(v)
	if len(runes) <= maxSheetCellChars {
		return v
	}
	return string(runes[:maxSheetCellChars]) + "…"
}

type sheetsInfoInput struct {
	SpreadsheetID string `json:"spreadsheet_id"`
}

type sheetsReadInput struct {
	SpreadsheetID string `json:"spreadsheet_id"`
	Range         string `json:"range"`
	MaxRows       int    `json:"max_rows,omitempty"`
}

type sheetsAppendInput struct {
	SpreadsheetID string     `json:"spreadsheet_id"`
	Range         string     `json:"range"`
	Rows          [][]string `json:"rows"`
}
