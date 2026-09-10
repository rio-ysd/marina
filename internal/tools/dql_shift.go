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
	Name      string `json:"name"`
	BeryxName string `json:"beryx_name"`
	Month     string `json:"month"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// NewDQLShiftComplianceTools は、dqlのシフト予定とberyx_productionの実働記録(reports)を突き合わせ、
// シフト時間外に働いていないか調べるBetaToolセットを構築します。
// シフト予定=dql.shifts、実働時間=beryx_production.reports(全プロジェクト)という前提です。
// 氏名解決・UTC→JST変換・JOINをツール内で完結させます。
func NewDQLShiftComplianceTools(db *sql.DB) ([]anthropic.BetaTool, error) {
	tool, err := toolrunner.NewBetaToolFromBytes[dqlShiftComplianceInput](
		"dql_check_shift_compliance",
		"スタッフについて、beryx_production.reportsの実働記録(全プロジェクト合算)がdql.shiftsのシフト予定時間外に"+
			"及んでいないか調べる。nameはdqlでのスタッフの名前(部分一致、例: 藤原、MAHO)。"+
			"dqlのニックネームとberyx_production.usersの本名が異なる場合(例: dqlでMAHO/beryxで鈴木瑞希)は、"+
			"beryx_nameにberyx_production側の本名を渡す(省略時はnameと同じ値で検索する)。"+
			"fromとtoは省略可(YYYY-MM-DD、JST)で、省略時は直近"+fmt.Sprint(dqlShiftDefaultRangeDays)+"日を対象にする。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "dqlでのスタッフの名前(部分一致)"},
				"beryx_name": map[string]any{"type": "string", "description": "beryx_production側の本名(部分一致、nameと異なる場合のみ指定)"},
				"from":       map[string]any{"type": "string", "description": "調査開始日(YYYY-MM-DD、JST、省略可)"},
				"to":         map[string]any{"type": "string", "description": "調査終了日(YYYY-MM-DD、JST、省略可、既定は今日)"},
			},
			"required": []string{"name"},
		}),
		func(ctx context.Context, in dqlShiftComplianceInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			text, err := checkDQLShiftCompliance(ctx, db, in.Name, in.BeryxName, in.From, in.To)
			if err != nil {
				return textResult(fmt.Sprintf("調査に失敗しました: %v", err)), nil
			}
			return verbatimResult(text), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []anthropic.BetaTool{tool}, nil
}

// dqlStaff はシフト予定を持つdql側のスタッフ(dql.admins経由)です。
type dqlStaff struct {
	id       int64
	name     string
	lastName sql.NullString
}

func (s dqlStaff) label() string {
	if s.lastName.Valid && s.lastName.String != "" {
		return fmt.Sprintf("%s%s(dql id=%d)", s.lastName.String, s.name, s.id)
	}
	return fmt.Sprintf("%s(dql id=%d)", s.name, s.id)
}

// plainName はID等を含まない表示名(姓名またはニックネーム)を返します。
func (s dqlStaff) plainName() string {
	if s.lastName.Valid && s.lastName.String != "" {
		return s.lastName.String + s.name
	}
	return s.name
}

// findDQLStaff はdql.adminsにレコードがあるusersのみを対象に名前の部分一致で検索します。
func findDQLStaff(ctx context.Context, db *sql.DB, name string) ([]dqlStaff, error) {
	like := "%" + name + "%"
	// 「藤原香織」のように姓名がスペース無しで渡されても、name/last_nameが別カラムなのでヒットするよう、
	// スペースを除いた姓名の連結(藤原+香織 / 香織+藤原の両順)とも比較する。
	likeNoSpace := "%" + strings.ReplaceAll(name, " ", "") + "%"
	rows, err := db.QueryContext(ctx, `
		SELECT u.id, u.name, u.last_name
		FROM dql.users u
		JOIN dql.admins a ON a.user_id = u.id
		WHERE u.name LIKE ? OR u.last_name LIKE ?
		   OR REPLACE(CONCAT(IFNULL(u.last_name, ''), u.name), ' ', '') LIKE ?
		   OR REPLACE(CONCAT(u.name, IFNULL(u.last_name, '')), ' ', '') LIKE ?`,
		like, like, likeNoSpace, likeNoSpace)
	if err != nil {
		return nil, fmt.Errorf("find dql staff: %w", err)
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

// beryxUser はberyx_production側の実働記録(reports)を持つ社員です。
type beryxUser struct {
	id   int64
	name string
}

// findBeryxUser はberyx_production.usersを名前の部分一致で検索します。
func findBeryxUser(ctx context.Context, db *sql.DB, name string) ([]beryxUser, error) {
	like := "%" + name + "%"
	// beryx_production.usersのnameは"姓 名"(スペース区切り)のため、スペース無しの検索語でもヒットするようにする。
	likeNoSpace := "%" + strings.ReplaceAll(name, " ", "") + "%"
	rows, err := db.QueryContext(ctx, `
		SELECT id, name FROM beryx_production.users
		WHERE name LIKE ? OR REPLACE(name, ' ', '') LIKE ?`, like, likeNoSpace)
	if err != nil {
		return nil, fmt.Errorf("find beryx user: %w", err)
	}
	defer rows.Close()

	var result []beryxUser
	for rows.Next() {
		var u beryxUser
		if err := rows.Scan(&u.id, &u.name); err != nil {
			return nil, err
		}
		result = append(result, u)
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

// timeRange は時刻の範囲(重複区間の記録用)です。
type timeRange struct {
	start time.Time
	end   time.Time
}

type dqlShiftViolation struct {
	start    time.Time
	end      time.Time
	startMin sql.NullInt64
	endMin   sql.NullInt64
	// overlaps はこの区間にマージされた元のberyx_production.reports行同士が重複していた範囲です。
	// 連続する行が隙間なくつながっているだけ(重複なし)の場合は空です。
	overlaps []timeRange
}

// mergeReportIntervals は同じ日の実働区間(started_at昇順)を、隙間なく連続/重複するものだけ1つにまとめます。
// 重複があった場合はoverlapsにその重複範囲を記録します。日をまたぐ・空白期間がある行は別区間のままです。
func mergeReportIntervals(rows []dqlShiftViolation) []dqlShiftViolation {
	if len(rows) == 0 {
		return nil
	}
	merged := []dqlShiftViolation{rows[0]}
	for _, r := range rows[1:] {
		cur := &merged[len(merged)-1]
		if r.start.After(cur.end) {
			merged = append(merged, r)
			continue
		}
		if r.start.Before(cur.end) {
			overlapEnd := r.end
			if cur.end.Before(overlapEnd) {
				overlapEnd = cur.end
			}
			cur.overlaps = append(cur.overlaps, timeRange{start: r.start, end: overlapEnd})
		}
		if r.end.After(cur.end) {
			cur.end = r.end
		}
	}
	return merged
}

// shiftComplianceResolution はシフト遵守チェックの解決結果です。
// Messageが空でない場合は氏名解決が失敗/曖昧だったことを示し、それをそのままユーザーへ返します。
type shiftComplianceResolution struct {
	Message    string
	DQLStaff   dqlStaff
	BeryxUser  beryxUser
	From       string
	To         string
	Total      int
	Violations []dqlShiftViolation
}

// resolveShiftViolations は氏名解決からシフト時間外の実働抽出までを行います。
// dql_check_shift_complianceと防犯センサー付きサマリの両方から共通で使います。
func resolveShiftViolations(ctx context.Context, db *sql.DB, name, beryxName, fromStr, toStr string) (shiftComplianceResolution, error) {
	from, to, err := resolveDQLDateRange(fromStr, toStr)
	if err != nil {
		return shiftComplianceResolution{}, err
	}

	dqlCandidates, err := findDQLStaff(ctx, db, name)
	if err != nil {
		return shiftComplianceResolution{}, err
	}
	if len(dqlCandidates) == 0 {
		return shiftComplianceResolution{Message: fmt.Sprintf("「%s」に該当するシフト予定(dql.admins登録スタッフ)が見つかりませんでした。", name)}, nil
	}
	if len(dqlCandidates) > 1 {
		return shiftComplianceResolution{Message: ambiguousStaffMessage(name, dqlCandidates)}, nil
	}
	dqlStaffMatch := dqlCandidates[0]

	if beryxName == "" {
		beryxName = name
	}
	beryxCandidates, err := findBeryxUser(ctx, db, beryxName)
	if err != nil {
		return shiftComplianceResolution{}, err
	}
	if len(beryxCandidates) == 0 {
		return shiftComplianceResolution{Message: fmt.Sprintf("「%s」に該当する実働記録(beryx_production.users)が見つかりませんでした。dqlのニックネームと本名が異なる場合はberyx_nameで本名を指定してください。", beryxName)}, nil
	}
	if len(beryxCandidates) > 1 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("「%s」に複数の社員が該当しました(beryx_production)。名前を確認してください:\n", beryxName))
		for _, u := range beryxCandidates {
			b.WriteString(fmt.Sprintf("- %s(beryx id=%d)\n", u.name, u.id))
		}
		return shiftComplianceResolution{Message: b.String()}, nil
	}
	beryxUserMatch := beryxCandidates[0]

	// beryx_production.reports.started_at/ended_atは(dqlのDATETIME列と異なり)JSTでそのまま保存されているため変換不要。
	// shift_dateはJSTの日付そのままなので、started_atをそのままDATE()で日付比較する。
	rows, err := db.QueryContext(ctx, `
		SELECT r.started_at, r.ended_at, sh.start_min, sh.end_min
		FROM beryx_production.reports r
		JOIN beryx_production.members m ON m.id = r.member_id
		LEFT JOIN (
			SELECT shift_date, MIN(shift_hour) AS start_min, MAX(shift_hour) + 30 AS end_min
			FROM dql.shifts
			WHERE user_id = ?
			GROUP BY shift_date
		) sh ON sh.shift_date = DATE(r.started_at)
		WHERE m.user_id = ?
		  AND r.started_at >= ?
		  AND r.started_at <  DATE_ADD(?, INTERVAL 1 DAY)
		ORDER BY r.started_at`,
		dqlStaffMatch.id, beryxUserMatch.id, from, to)
	if err != nil {
		return shiftComplianceResolution{}, fmt.Errorf("query reports: %w", err)
	}
	defer rows.Close()

	var total int
	var allRows []dqlShiftViolation
	for rows.Next() {
		var v dqlShiftViolation
		if err := rows.Scan(&v.start, &v.end, &v.startMin, &v.endMin); err != nil {
			return shiftComplianceResolution{}, err
		}
		total++
		allRows = append(allRows, v)
	}
	if err := rows.Err(); err != nil {
		return shiftComplianceResolution{}, err
	}

	// 同じ日の行(started_at昇順で並んでいる)ごとにまとめてから、隙間なく連続/重複する区間をマージする。
	var violations []dqlShiftViolation
	var dayRows []dqlShiftViolation
	flushDay := func() {
		for _, seg := range mergeReportIntervals(dayRows) {
			if isOutsideShift(seg) {
				violations = append(violations, seg)
			}
		}
		dayRows = nil
	}
	for _, v := range allRows {
		if len(dayRows) > 0 && !sameDate(dayRows[0].start, v.start) {
			flushDay()
		}
		dayRows = append(dayRows, v)
	}
	flushDay()

	return shiftComplianceResolution{
		DQLStaff:   dqlStaffMatch,
		BeryxUser:  beryxUserMatch,
		From:       from,
		To:         to,
		Total:      total,
		Violations: violations,
	}, nil
}

func checkDQLShiftCompliance(ctx context.Context, db *sql.DB, name, beryxName, fromStr, toStr string) (string, error) {
	res, err := resolveShiftViolations(ctx, db, name, beryxName, fromStr, toStr)
	if err != nil {
		return "", err
	}
	if res.Message != "" {
		return res.Message, nil
	}

	staffLabel := fmt.Sprintf("%s / %s(beryx id=%d)", res.DQLStaff.label(), res.BeryxUser.name, res.BeryxUser.id)
	rangeLabel := fmt.Sprintf("%sから%s", res.From, res.To)
	if res.Total == 0 {
		return fmt.Sprintf("%sの%sの実働記録(beryx_production.reports)はありませんでした。", staffLabel, rangeLabel), nil
	}
	if len(res.Violations) == 0 {
		return fmt.Sprintf("%sの%s(実働記録%d件)は、すべてシフト時間内でした。", staffLabel, rangeLabel, res.Total), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%sの%s(実働記録%d件)のうち、シフト時間外の勤務が%d件見つかりました:\n", staffLabel, rangeLabel, res.Total, len(res.Violations)))
	for _, v := range res.Violations {
		shiftLabel := "その日はシフト登録なし"
		if v.startMin.Valid {
			shiftLabel = "シフトは" + formatMinuteRange(v.startMin.Int64, v.endMin.Int64)
		}
		b.WriteString(fmt.Sprintf("- %s〜%s (%s)\n", v.start.Format("2006-01-02 15:04"), v.end.Format("15:04"), shiftLabel))
		for _, ov := range v.overlaps {
			b.WriteString(fmt.Sprintf("  時間重複: %s（%s〜%s）\n", formatDurationJa(ov.end.Sub(ov.start)), ov.start.Format("15:04"), ov.end.Format("15:04")))
		}
	}
	return b.String(), nil
}

// isOutsideShift はreportの実働区間がその日のシフト範囲を外れている(開始が早い/終了が遅い/シフト自体が無い)かを返します。
func isOutsideShift(v dqlShiftViolation) bool {
	if !v.startMin.Valid {
		return true
	}
	day := time.Date(v.start.Year(), v.start.Month(), v.start.Day(), 0, 0, 0, 0, v.start.Location())
	shiftStart := day.Add(time.Duration(v.startMin.Int64) * time.Minute)
	shiftEnd := day.Add(time.Duration(v.endMin.Int64) * time.Minute)
	return v.start.Before(shiftStart) || v.end.After(shiftEnd)
}

// sameDate はtとuが同じ日(年月日)かを返します。
func sameDate(t, u time.Time) bool {
	ty, tm, td := t.Date()
	uy, um, ud := u.Date()
	return ty == uy && tm == um && td == ud
}

func ambiguousStaffMessage(name string, candidates []dqlStaff) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("「%s」に複数のスタッフが該当しました(dql)。名前を確認してください:\n", name))
	for _, s := range candidates {
		b.WriteString("- " + s.label() + "\n")
	}
	return b.String()
}

// verbatimResult は数値を含むツール結果を、Claudeが計算し直したり言い換えたりしないよう強い注意を付けて返します。
// system promptの一般的な指示だけでは数値が書き換わってしまうことがあったため、データの直前に埋め込む。
func verbatimResult(text string) anthropic.BetaToolResultBlockParamContentUnion {
	const notice = "重要: 以下は一字一句そのままSlackへの返信に使ってください。" +
		"時刻・時間差・日付を自分で計算し直したり、要約したりしないでください(変更すると誤りになります)。\n\n"
	return textResult(notice + text)
}
