package tools

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type monthlyAttendanceDay struct {
	day           time.Time
	shiftStart    sql.NullInt64
	shiftEnd      sql.NullInt64
	reports       []dqlShiftViolation
	restMinutes   int64
	mergedReports []dqlShiftViolation
}

func currentJSTMonth() string {
	return time.Now().In(cameraNotifyJST).Format("2006-01")
}

func resolveMonth(month string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01", month, cameraNotifyJST)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("monthの形式が正しくありません(YYYY-MM): %w", err)
	}
	end := start.AddDate(0, 1, -1)
	now := time.Now().In(cameraNotifyJST)
	if start.Year() == now.Year() && start.Month() == now.Month() && now.Before(end.AddDate(0, 0, 1)) {
		end = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cameraNotifyJST)
	}
	return start, end, nil
}

func buildMonthlyStaffAttendanceSummary(ctx context.Context, db *sql.DB, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID, name, beryxName, month string) (string, error) {
	monthStart, reportEnd, err := resolveMonth(month)
	if err != nil {
		return "", err
	}
	calendarEnd := monthStart.AddDate(0, 1, -1)

	dqlCandidates, err := findDQLStaff(ctx, db, name)
	if err != nil {
		return "", err
	}
	if len(dqlCandidates) == 0 {
		return fmt.Sprintf("「%s」に該当するシフト予定(dql.admins登録スタッフ)が見つかりませんでした。", name), nil
	}
	if len(dqlCandidates) > 1 {
		return ambiguousStaffMessage(name, dqlCandidates), nil
	}
	dqlStaffMatch := dqlCandidates[0]

	if beryxName == "" {
		beryxName = name
	}
	beryxCandidates, err := findBeryxUser(ctx, db, beryxName)
	if err != nil {
		return "", err
	}
	if len(beryxCandidates) == 0 {
		return fmt.Sprintf("「%s」に該当する実働記録(beryx_production.users)が見つかりませんでした。dqlのニックネームと本名が異なる場合はberyx_nameで本名を指定してください。", beryxName), nil
	}
	if len(beryxCandidates) > 1 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("「%s」に複数の社員が該当しました(beryx_production)。名前を確認してください:\n", beryxName))
		for _, user := range beryxCandidates {
			b.WriteString(fmt.Sprintf("- %s(beryx id=%d)\n", user.name, user.id))
		}
		return b.String(), nil
	}

	days, err := loadMonthlyAttendanceDays(ctx, db, dqlStaffMatch.id, beryxCandidates[0].id, monthStart, calendarEnd, reportEnd)
	if err != nil {
		return "", err
	}
	return formatMonthlyAttendanceReport(ctx, cameraClient, cameraChannelID, cameraUserID, dqlStaffMatch.plainName(), monthStart, reportEnd, days)
}

func loadMonthlyAttendanceDays(ctx context.Context, db *sql.DB, dqlUserID, beryxUserID int64, monthStart, shiftEnd, reportEnd time.Time) (map[string]*monthlyAttendanceDay, error) {
	days := make(map[string]*monthlyAttendanceDay)
	shiftRows, err := db.QueryContext(ctx, `
		SELECT shift_date, MIN(shift_hour), MAX(shift_hour) + 30
		FROM dql.shifts
		WHERE user_id = ? AND shift_date >= ? AND shift_date <= ?
		GROUP BY shift_date
		ORDER BY shift_date`, dqlUserID, monthStart.Format("2006-01-02"), shiftEnd.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("query monthly shifts: %w", err)
	}
	for shiftRows.Next() {
		var date string
		var start, end int64
		if err := shiftRows.Scan(&date, &start, &end); err != nil {
			shiftRows.Close()
			return nil, err
		}
		day, err := time.ParseInLocation("2006-01-02", date, cameraNotifyJST)
		if err != nil {
			shiftRows.Close()
			return nil, err
		}
		days[date] = &monthlyAttendanceDay{day: day, shiftStart: sql.NullInt64{Int64: start, Valid: true}, shiftEnd: sql.NullInt64{Int64: end, Valid: true}}
	}
	if err := shiftRows.Err(); err != nil {
		shiftRows.Close()
		return nil, err
	}
	shiftRows.Close()

	reportRows, err := db.QueryContext(ctx, `
		SELECT DATE_FORMAT(r.started_at, '%Y-%m-%d'), r.started_at, r.ended_at, CAST(TIME_TO_SEC(COALESCE(r.rest_time, '00:00:00')) / 60 AS SIGNED)
		FROM beryx_production.reports r
		JOIN beryx_production.members m ON m.id = r.member_id
		WHERE m.user_id = ?
		  AND r.started_at >= ?
		  AND r.started_at < DATE_ADD(?, INTERVAL 1 DAY)
		ORDER BY r.started_at`, beryxUserID, monthStart.Format("2006-01-02"), reportEnd.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("query monthly reports: %w", err)
	}
	for reportRows.Next() {
		var date string
		var report dqlShiftViolation
		var restMinutes sql.NullInt64
		if err := reportRows.Scan(&date, &report.start, &report.end, &restMinutes); err != nil {
			reportRows.Close()
			return nil, err
		}
		if _, ok := days[date]; !ok {
			day, err := time.ParseInLocation("2006-01-02", date, cameraNotifyJST)
			if err != nil {
				reportRows.Close()
				return nil, err
			}
			days[date] = &monthlyAttendanceDay{day: day}
		}
		days[date].reports = append(days[date].reports, report)
		if restMinutes.Valid {
			days[date].restMinutes += restMinutes.Int64
		}
	}
	if err := reportRows.Err(); err != nil {
		reportRows.Close()
		return nil, err
	}
	reportRows.Close()

	for _, day := range days {
		day.mergedReports = mergeReportIntervals(day.reports)
	}
	return days, nil
}

func formatMonthlyAttendanceReport(ctx context.Context, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID, displayName string, monthStart, reportEnd time.Time, days map[string]*monthlyAttendanceDay) (string, error) {
	orderedDays := make([]*monthlyAttendanceDay, 0, len(days))
	for _, day := range days {
		orderedDays = append(orderedDays, day)
	}
	sort.Slice(orderedDays, func(i, j int) bool { return orderedDays[i].day.Before(orderedDays[j].day) })

	var b strings.Builder
	b.WriteString(fmt.Sprintf("- %s(契約時間/週: 要確認, 基準時間/日: 要確認)\n", displayName))
	b.WriteString("  - 調査対象月\n")
	b.WriteString(fmt.Sprintf("    - %s\n", formatAttendanceMonthLabel(monthStart, reportEnd)))
	b.WriteString("  - 期間サマリ\n")
	b.WriteString(fmt.Sprintf("    - シフト日数: %d日\n", countShiftDays(orderedDays)))

	noReportCount, shortCount, outsideCount, breakCount := 0, 0, 0, 0
	var totalWorkMinutes int64
	for _, day := range orderedDays {
		if !day.day.After(reportEnd) && len(day.mergedReports) == 0 && day.shiftStart.Valid {
			noReportCount++
		}
		if day.shiftStart.Valid && isShortAttendance(day) {
			shortCount++
		}
		if hasOutsideShift(day) {
			outsideCount++
		}
		if hasInsufficientBreak(day) {
			breakCount++
		}
		totalWorkMinutes += actualWorkMinutes(day)
	}
	b.WriteString(fmt.Sprintf("    - 実働記録なし: %d日\n", noReportCount))
	b.WriteString(fmt.Sprintf("    - シフト不足: %d日\n", shortCount))
	b.WriteString(fmt.Sprintf("    - シフト時間外: %d日\n", outsideCount))
	b.WriteString(fmt.Sprintf("    - 休憩不足: %d日\n", breakCount))
	b.WriteString("    - 有給・休日申請: 要確認\n")
	b.WriteString("    - 振替出勤: 要確認\n")
	b.WriteString("    - 振替未解消: 要確認\n")

	b.WriteString("  - 月別確認\n")
	b.WriteString(fmt.Sprintf("    - %s\n", formatAttendanceMonthLabel(monthStart, reportEnd)))
	b.WriteString(fmt.Sprintf("      - シフト日数: %d日\n", countShiftDays(orderedDays)))
	b.WriteString(fmt.Sprintf("      - 稼働時間: %s / 契約時間: 要確認\n", formatDurationJa(time.Duration(totalWorkMinutes)*time.Minute)))
	b.WriteString("      - 有給・休日申請: 要確認\n")
	b.WriteString("      - 振替出勤: 要確認\n")
	b.WriteString("      - 問題点\n")
	if noReportCount == 0 && shortCount == 0 && outsideCount == 0 && breakCount == 0 {
		b.WriteString("        - なし\n")
	} else {
		if noReportCount > 0 {
			b.WriteString(fmt.Sprintf("        - 実働記録なし: %d日\n", noReportCount))
		}
		if shortCount > 0 {
			b.WriteString(fmt.Sprintf("        - シフト不足: %d日\n", shortCount))
		}
		if outsideCount > 0 {
			b.WriteString(fmt.Sprintf("        - シフト時間外: %d日\n", outsideCount))
		}
		if breakCount > 0 {
			b.WriteString(fmt.Sprintf("        - 休憩不足: %d日\n", breakCount))
		}
	}

	b.WriteString("  - 日別\n")
	if len(orderedDays) == 0 {
		b.WriteString("    - なし\n")
	}
	for _, day := range orderedDays {
		if err := appendAttendanceDay(ctx, &b, cameraClient, cameraChannelID, cameraUserID, reportEnd, day); err != nil {
			return "", err
		}
	}
	appendWeeklyAttendanceSummary(&b, reportEnd, orderedDays)
	return b.String(), nil
}

func formatAttendanceMonthLabel(monthStart, monthEnd time.Time) string {
	calendarEnd := monthStart.AddDate(0, 1, -1)
	label := fmt.Sprintf("%s年%d月", monthStart.Format("2006"), monthStart.Month())
	if monthEnd.Before(calendarEnd) {
		label += fmt.Sprintf("(%d/%d時点)", monthEnd.Month(), monthEnd.Day())
	}
	return label
}

func countShiftDays(days []*monthlyAttendanceDay) int {
	count := 0
	for _, day := range days {
		if day.shiftStart.Valid {
			count++
		}
	}
	return count
}

func actualWorkMinutes(day *monthlyAttendanceDay) int64 {
	var minutes int64
	for _, report := range day.mergedReports {
		minutes += int64(report.end.Sub(report.start).Minutes())
	}
	minutes -= day.restMinutes
	if minutes < 0 {
		return 0
	}
	return minutes
}

func isShortAttendance(day *monthlyAttendanceDay) bool {
	if !day.shiftStart.Valid || len(day.mergedReports) == 0 {
		return false
	}
	return actualWorkMinutes(day) < day.shiftEnd.Int64-day.shiftStart.Int64
}

func hasOutsideShift(day *monthlyAttendanceDay) bool {
	for _, report := range day.mergedReports {
		if isOutsideShift(dqlShiftViolation{start: report.start, end: report.end, startMin: day.shiftStart, endMin: day.shiftEnd}) {
			return true
		}
	}
	return false
}

func hasInsufficientBreak(day *monthlyAttendanceDay) bool {
	return actualWorkMinutes(day) >= 360 && day.restMinutes < 60
}

func appendAttendanceDay(ctx context.Context, b *strings.Builder, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID string, reportEnd time.Time, day *monthlyAttendanceDay) error {
	b.WriteString(fmt.Sprintf("    - %s\n", day.day.Format("1/2")))
	shiftLabel := "シフト登録なし"
	if day.shiftStart.Valid {
		shiftLabel = formatMinuteRange(day.shiftStart.Int64, day.shiftEnd.Int64)
	}
	if day.day.After(reportEnd) {
		b.WriteString(fmt.Sprintf("      - 実働 / シフト\n        - 未来 / %s\n", shiftLabel))
		b.WriteString("      - 問題点\n        - なし\n")
		return nil
	}
	if len(day.mergedReports) == 0 {
		b.WriteString(fmt.Sprintf("      - 実働なし / シフト %s\n", shiftLabel))
		b.WriteString("      - 問題点\n        - 実働記録なし\n          - 欠勤・有給・休日申請を確認\n")
		return nil
	}

	first := day.mergedReports[0].start
	last := day.mergedReports[len(day.mergedReports)-1].end
	b.WriteString(fmt.Sprintf("      - 実働 / シフト\n        - %s-%s / %s\n", first.Format("15:04"), last.Format("15:04"), shiftLabel))
	b.WriteString("      - 問題点\n")
	hasIssue := false
	if day.shiftStart.Valid {
		shiftStart := day.day.Add(time.Duration(day.shiftStart.Int64) * time.Minute)
		shiftEnd := day.day.Add(time.Duration(day.shiftEnd.Int64) * time.Minute)
		if first.After(shiftStart) {
			hasIssue = true
			b.WriteString(fmt.Sprintf("        - 遅刻\n          - %s\n", formatDurationJa(first.Sub(shiftStart))))
		}
		if last.Before(shiftEnd) {
			hasIssue = true
			b.WriteString(fmt.Sprintf("        - 早退\n          - %s\n", formatDurationJa(shiftEnd.Sub(last))))
		}
	}
	if isShortAttendance(day) {
		hasIssue = true
		shortage := day.shiftEnd.Int64 - day.shiftStart.Int64 - actualWorkMinutes(day)
		if shortage < 0 {
			shortage = 0
		}
		b.WriteString(fmt.Sprintf("        - シフト不足\n          - %s\n", formatDurationJa(time.Duration(shortage)*time.Minute)))
	}
	if hasOutsideShift(day) {
		hasIssue = true
		b.WriteString("        - シフト時間外\n")
		if !day.shiftStart.Valid {
			b.WriteString("          - その日はシフト登録なし\n")
		} else {
			shiftStart := day.day.Add(time.Duration(day.shiftStart.Int64) * time.Minute)
			shiftEnd := day.day.Add(time.Duration(day.shiftEnd.Int64) * time.Minute)
			if first.Before(shiftStart) {
				b.WriteString(fmt.Sprintf("          - 開始%s早い\n", formatDurationJa(shiftStart.Sub(first))))
			}
			if last.After(shiftEnd) {
				b.WriteString(fmt.Sprintf("          - 終了%s遅い\n", formatDurationJa(last.Sub(shiftEnd))))
			}
		}
	}
	for _, report := range day.mergedReports {
		for _, overlap := range report.overlaps {
			hasIssue = true
			b.WriteString(fmt.Sprintf("        - 時間重複\n          - %s(%s-%s)\n", formatDurationJa(overlap.end.Sub(overlap.start)), overlap.start.Format("15:04"), overlap.end.Format("15:04")))
		}
	}
	if hasInsufficientBreak(day) {
		hasIssue = true
		b.WriteString(fmt.Sprintf("        - 休憩不足\n          - 必要1時間 / 実績%s\n", formatDurationJa(time.Duration(day.restMinutes)*time.Minute)))
	}

	camFirst, camLast, camCount, err := firstLastNotification(ctx, cameraClient, cameraChannelID, cameraUserID, day.day, day.day.AddDate(0, 0, 1))
	if err != nil {
		return fmt.Errorf("get camera notifications: %w", err)
	}
	if camCount > 0 {
		camFirst = camFirst.In(cameraNotifyJST)
		camLast = camLast.In(cameraNotifyJST)
		startDiff := camFirst.Sub(first)
		endDiff := camLast.Sub(last)
		if startDiff > 0 || endDiff < 0 {
			hasIssue = true
		}
		b.WriteString(fmt.Sprintf("        - 防犯センサー\n          - %s-%s\n", camFirst.Format("15:04"), camLast.Format("15:04")))
		if startDiff > 0 {
			b.WriteString(fmt.Sprintf("          - 開始%s遅い\n", formatDurationJa(startDiff)))
		}
		if endDiff < 0 {
			b.WriteString(fmt.Sprintf("          - 終了%s早い\n", formatDurationJa(-endDiff)))
		}
	}
	if !hasIssue {
		b.WriteString("        - なし\n")
	}
	b.WriteString("      - 申請・承認\n        - 残業申請: 要確認\n        - 残業理由: 要確認\n        - 店長承認: 要確認\n        - 有給・休日・振替: 要確認\n")
	return nil
}

func appendWeeklyAttendanceSummary(b *strings.Builder, reportEnd time.Time, days []*monthlyAttendanceDay) {
	weeks := make(map[string][]*monthlyAttendanceDay)
	for _, day := range days {
		if day.day.After(reportEnd) {
			continue
		}
		year, week := day.day.ISOWeek()
		key := fmt.Sprintf("%04d-W%02d", year, week)
		weeks[key] = append(weeks[key], day)
	}
	var keys []string
	for key := range weeks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		weekDays := weeks[key]
		shiftDays := countShiftDays(weekDays)
		if shiftDays >= 5 {
			continue
		}
		b.WriteString("  - 週別確認（該当する週のみ）\n")
		sort.Slice(weekDays, func(i, j int) bool { return weekDays[i].day.Before(weekDays[j].day) })
		b.WriteString(fmt.Sprintf("    - %s〜%s\n", weekDays[0].day.Format("2006-01-02"), weekDays[len(weekDays)-1].day.Format("2006-01-02")))
		b.WriteString(fmt.Sprintf("      - シフト日数: %d日\n", shiftDays))
		b.WriteString("      - 稼働時間: 要確認 / 契約時間: 要確認\n")
		b.WriteString("      - 問題点\n        - 週5日未満\n")
		b.WriteString(fmt.Sprintf("          - %d日\n", shiftDays))
	}
}
