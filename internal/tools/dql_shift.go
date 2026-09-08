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

// dqlShiftDefaultRangeDays はfrom/to省略時に遡る日数です。
const dqlShiftDefaultRangeDays = 30

type dqlShiftComplianceInput struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// NewDQLShiftComplianceTools は、dqlのスタッフの完了済み予約がシフト時間外に行われていないか調べる
// BetaToolセットを構築します。氏名解決・UTC→JST変換・シフトとのJOINをツール内で完結させます。
func NewDQLShiftComplianceTools(db *sql.DB) ([]anthropic.BetaTool, error) {
	tool, err := toolrunner.NewBetaToolFromBytes[dqlShiftComplianceInput](
		"dql_check_shift_compliance",
		"dql(サロン予約管理)のスタッフについて、完了済み予約(施術)がシフト時間外に行われていないか調べる。"+
			"nameはスタッフの名前(name/last_nameの一部一致、例: 藤原、MAHO)。"+
			"fromとtoは省略可(YYYY-MM-DD、JST)で、省略時は直近"+fmt.Sprint(dqlShiftDefaultRangeDays)+"日を対象にする。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "スタッフの名前(部分一致)"},
				"from": map[string]any{"type": "string", "description": "調査開始日(YYYY-MM-DD、JST、省略可)"},
				"to":   map[string]any{"type": "string", "description": "調査終了日(YYYY-MM-DD、JST、省略可、既定は今日)"},
			},
			"required": []string{"name"},
		}),
		func(ctx context.Context, in dqlShiftComplianceInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			text, err := checkDQLShiftCompliance(ctx, db, in.Name, in.From, in.To)
			if err != nil {
				return textResult(fmt.Sprintf("調査に失敗しました: %v", err)), nil
			}
			return textResult(text), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []anthropic.BetaTool{tool}, nil
}

type dqlStaff struct {
	id       int64
	name     string
	lastName sql.NullString
}

func (s dqlStaff) label() string {
	if s.lastName.Valid && s.lastName.String != "" {
		return fmt.Sprintf("%s%s(id=%d)", s.lastName.String, s.name, s.id)
	}
	return fmt.Sprintf("%s(id=%d)", s.name, s.id)
}

// findDQLStaff はdql.adminsにレコードがあるusersのみを対象に名前の部分一致で検索します。
func findDQLStaff(ctx context.Context, db *sql.DB, name string) ([]dqlStaff, error) {
	like := "%" + name + "%"
	rows, err := db.QueryContext(ctx, `
		SELECT u.id, u.name, u.last_name
		FROM dql.users u
		JOIN dql.admins a ON a.user_id = u.id
		WHERE u.name LIKE ? OR u.last_name LIKE ?`, like, like)
	if err != nil {
		return nil, fmt.Errorf("find staff: %w", err)
	}
	defer rows.Close()

	var result []dqlStaff
	for rows.Next() {
		var s dqlStaff
		if err := rows.Scan(&s.id, &s.name, &s.lastName); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// resolveDQLDateRange はfrom/to(YYYY-MM-DD、JST)を解決します。省略時はtoを今日、fromをそこから
// dqlShiftDefaultRangeDays日前とします。
func resolveDQLDateRange(fromStr, toStr string) (from, to string, err error) {
	toDay := time.Now().In(cameraNotifyJST)
	if toStr != "" {
		toDay, err = time.ParseInLocation("2006-01-02", toStr, cameraNotifyJST)
		if err != nil {
			return "", "", fmt.Errorf("toの形式が正しくありません: %w", err)
		}
	}
	fromDay := toDay.AddDate(0, 0, -dqlShiftDefaultRangeDays)
	if fromStr != "" {
		fromDay, err = time.ParseInLocation("2006-01-02", fromStr, cameraNotifyJST)
		if err != nil {
			return "", "", fmt.Errorf("fromの形式が正しくありません: %w", err)
		}
	}
	return fromDay.Format("2006-01-02"), toDay.Format("2006-01-02"), nil
}

// formatMinuteRange はその日0時からの分数の範囲を"HH:MM-HH:MM"表記にします。
func formatMinuteRange(startMin, endMin int64) string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", startMin/60, startMin%60, endMin/60, endMin%60)
}

type dqlShiftViolation struct {
	at       time.Time
	startMin sql.NullInt64
	endMin   sql.NullInt64
}

func checkDQLShiftCompliance(ctx context.Context, db *sql.DB, name, fromStr, toStr string) (string, error) {
	from, to, err := resolveDQLDateRange(fromStr, toStr)
	if err != nil {
		return "", err
	}

	staff, err := findDQLStaff(ctx, db, name)
	if err != nil {
		return "", err
	}
	if len(staff) == 0 {
		return fmt.Sprintf("「%s」に該当するスタッフ(dql.adminsに登録があるユーザー)が見つかりませんでした。", name), nil
	}
	if len(staff) > 1 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("「%s」に複数のスタッフが該当しました。名前を確認してください:\n", name))
		for _, s := range staff {
			b.WriteString("- " + s.label() + "\n")
		}
		return b.String(), nil
	}
	target := staff[0]

	// reservation_atはUTC保存のため、JSTの[from, to]の日境界をUTCへ変換して絞り込む。
	// shift_dateはJSTの日付そのままなので、reservation_atをJSTへ戻してから突き合わせる。
	rows, err := db.QueryContext(ctx, `
		SELECT DATE_ADD(r.reservation_at, INTERVAL 9 HOUR) AS jst_at, sh.start_min, sh.end_min
		FROM dql.reservations r
		LEFT JOIN (
			SELECT user_id, shift_date, MIN(shift_hour) AS start_min, MAX(shift_hour) + 30 AS end_min
			FROM dql.shifts
			WHERE user_id = ?
			GROUP BY user_id, shift_date
		) sh ON sh.user_id = ? AND sh.shift_date = DATE(DATE_ADD(r.reservation_at, INTERVAL 9 HOUR))
		WHERE r.staff_user_id = ? AND r.status = 2
		  AND r.reservation_at >= DATE_SUB(?, INTERVAL 9 HOUR)
		  AND r.reservation_at <  DATE_SUB(DATE_ADD(?, INTERVAL 1 DAY), INTERVAL 9 HOUR)
		ORDER BY r.reservation_at`,
		target.id, target.id, target.id, from, to)
	if err != nil {
		return "", fmt.Errorf("query reservations: %w", err)
	}
	defer rows.Close()

	var total int
	var violations []dqlShiftViolation
	for rows.Next() {
		var v dqlShiftViolation
		if err := rows.Scan(&v.at, &v.startMin, &v.endMin); err != nil {
			return "", err
		}
		total++
		minuteOfDay := int64(v.at.Hour()*60 + v.at.Minute())
		if !v.startMin.Valid || minuteOfDay < v.startMin.Int64 || minuteOfDay >= v.endMin.Int64 {
			violations = append(violations, v)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	rangeLabel := fmt.Sprintf("%sから%s", from, to)
	if total == 0 {
		return fmt.Sprintf("%sの%sの完了済み予約はありませんでした。", target.label(), rangeLabel), nil
	}
	if len(violations) == 0 {
		return fmt.Sprintf("%sの%s(完了済み予約%d件)は、すべてシフト時間内でした。", target.label(), rangeLabel, total), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%sの%s(完了済み予約%d件)のうち、シフト時間外の施術が%d件見つかりました:\n", target.label(), rangeLabel, total, len(violations)))
	for _, v := range violations {
		shiftLabel := "その日はシフト登録なし"
		if v.startMin.Valid {
			shiftLabel = "シフトは" + formatMinuteRange(v.startMin.Int64, v.endMin.Int64)
		}
		b.WriteString(fmt.Sprintf("- %s (%s)\n", v.at.Format("2006-01-02 15:04"), shiftLabel))
	}
	return b.String(), nil
}
