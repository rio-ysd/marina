package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
)

// dbSelectMaxRows は結果の返しすぎ(トークン肥大・意図しない全件ダンプ)を防ぐための上限行数です。
const dbSelectMaxRows = 200

// dbSelectQueryTimeout は1クエリあたりの実行時間の上限です。
const dbSelectQueryTimeout = 10 * time.Second

// dbSelectDeniedTables はトークンなど機密情報を含むため、問い合わせを許可しないテーブルです。
var dbSelectDeniedTables = []string{"oauth_tokens"}

type dbSelectQueryInput struct {
	Query string `json:"query"`
}

// NewDBQueryTools はDBに対する読み取り専用のSELECTクエリを実行するBetaToolセットを構築します。
func NewDBQueryTools(db *sql.DB) ([]anthropic.BetaTool, error) {
	tool, err := toolrunner.NewBetaToolFromBytes[dbSelectQueryInput](
		"db_select_query",
		"外部DBに対してSELECT文を1つだけ実行し、結果を返す(最大"+fmt.Sprint(dbSelectMaxRows)+"行)。"+
			"SELECT以外の文やセミコロン区切りの複数文は実行できない。テーブル構造が不明な場合はSHOW TABLES/DESCRIBEではなく"+
			"information_schema.columnsをSELECTで調べる。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "実行するSELECT文(1文のみ)"},
			},
			"required": []string{"query"},
		}),
		func(ctx context.Context, in dbSelectQueryInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			query, err := validateSelectQuery(in.Query)
			if err != nil {
				return textResult(err.Error()), nil
			}

			queryCtx, cancel := context.WithTimeout(ctx, dbSelectQueryTimeout)
			defer cancel()

			text, err := runSelectQuery(queryCtx, db, query)
			if err != nil {
				return textResult(fmt.Sprintf("クエリの実行に失敗しました: %v", err)), nil
			}
			return textResult(text), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []anthropic.BetaTool{tool}, nil
}

// validateSelectQuery はSELECT文1つだけであること、機密テーブルを含まないことを確認します。
func validateSelectQuery(query string) (string, error) {
	trimmed := strings.TrimSpace(query)
	trimmed = strings.TrimSuffix(trimmed, ";")
	if strings.Contains(trimmed, ";") {
		return "", fmt.Errorf("セミコロン区切りの複数文は実行できません。SELECT文を1つだけ渡してください。")
	}
	if !strings.HasPrefix(strings.ToUpper(trimmed), "SELECT") {
		return "", fmt.Errorf("SELECT文のみ実行できます。")
	}
	upper := strings.ToUpper(trimmed)
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "TRUNCATE", "GRANT", "REPLACE", "CALL", "INTO OUTFILE", "INTO DUMPFILE", "LOAD_FILE"} {
		if strings.Contains(upper, kw) {
			return "", fmt.Errorf("SELECT文以外の操作(%s)は実行できません。", kw)
		}
	}
	lower := strings.ToLower(trimmed)
	for _, table := range dbSelectDeniedTables {
		if strings.Contains(lower, table) {
			return "", fmt.Errorf("%sテーブルは機密情報を含むため問い合わせできません。", table)
		}
	}
	return trimmed, nil
}

// runSelectQuery はクエリを実行し、結果をタブ区切りのテキストへ整形します。
func runSelectQuery(ctx context.Context, db *sql.DB, query string) (string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(strings.Join(cols, "\t"))
	b.WriteString("\n")

	values := make([]sql.RawBytes, len(cols))
	scanArgs := make([]any, len(cols))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	rowCount := 0
	truncated := false
	for rows.Next() {
		if rowCount >= dbSelectMaxRows {
			truncated = true
			break
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return "", err
		}
		cells := make([]string, len(cols))
		for i, v := range values {
			if v == nil {
				cells[i] = "NULL"
			} else {
				cells[i] = string(v)
			}
		}
		b.WriteString(strings.Join(cells, "\t"))
		b.WriteString("\n")
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if rowCount == 0 {
		return "該当する行はありませんでした。", nil
	}
	if truncated {
		b.WriteString(fmt.Sprintf("(%d行で打ち切りました。絞り込み条件を追加してください)\n", dbSelectMaxRows))
	}
	return b.String(), nil
}
