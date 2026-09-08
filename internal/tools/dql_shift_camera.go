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

// NewStaffAttendanceSummaryTools は、シフト時間外の実働(dql_check_shift_complianceと同じ判定)に加えて、
// 監視カメラ通知チャンネルの防犯センサー検知時刻も付き合わせたサマリを返すBetaToolセットを構築します。
func NewStaffAttendanceSummaryTools(db *sql.DB, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID string) ([]anthropic.BetaTool, error) {
	tool, err := toolrunner.NewBetaToolFromBytes[dqlShiftComplianceInput](
		"check_staff_attendance_summary",
		"スタッフについて、シフト時間外の実働(dql_check_shift_complianceと同じ判定)を日付ごとに一覧化し、"+
			"あわせて監視カメラ通知チャンネルの防犯センサー検知時刻(最初/最後)も付き合わせて返す。"+
			"nameはdqlでのスタッフの名前(部分一致)。dqlのニックネームとberyx_production.usersの本名が異なる場合は"+
			"beryx_nameに本名を渡す。fromとtoは省略可(YYYY-MM-DD、JST)で、省略時は直近"+
			fmt.Sprint(dqlShiftDefaultRangeDays)+"日を対象にする。シフト時間外の実働が無い場合は「なし」とだけ返す。",
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
			text, err := buildStaffAttendanceSummary(ctx, db, cameraClient, cameraChannelID, cameraUserID, in.Name, in.BeryxName, in.From, in.To)
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

func buildStaffAttendanceSummary(ctx context.Context, db *sql.DB, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID, name, beryxName, fromStr, toStr string) (string, error) {
	res, err := resolveShiftViolations(ctx, db, name, beryxName, fromStr, toStr)
	if err != nil {
		return "", err
	}
	if res.Message != "" {
		return res.Message, nil
	}

	displayName := res.DQLStaff.plainName()
	if len(res.Violations) == 0 {
		return fmt.Sprintf("- %s\n  - なし\n", displayName), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("- %s\n", displayName))
	for _, v := range res.Violations {
		day := time.Date(v.start.Year(), v.start.Month(), v.start.Day(), 0, 0, 0, 0, v.start.Location())

		b.WriteString(fmt.Sprintf("  - %s\n", v.start.Format("1/2")))
		shiftLabel := "シフト登録なし"
		if v.startMin.Valid {
			shiftLabel = "シフト" + formatMinuteRange(v.startMin.Int64, v.endMin.Int64)
		}
		b.WriteString(fmt.Sprintf("    - 実働%s〜%s、%s\n", v.start.Format("15:04"), v.end.Format("15:04"), shiftLabel))
		b.WriteString("      - シフト時間外\n")
		if v.startMin.Valid {
			shiftStart := day.Add(time.Duration(v.startMin.Int64) * time.Minute)
			shiftEnd := day.Add(time.Duration(v.endMin.Int64) * time.Minute)
			if v.start.Before(shiftStart) {
				b.WriteString(fmt.Sprintf("        - 開始%s早い\n", formatDurationJa(shiftStart.Sub(v.start))))
			}
			if v.end.After(shiftEnd) {
				b.WriteString(fmt.Sprintf("        - 終了%s遅い\n", formatDurationJa(v.end.Sub(shiftEnd))))
			}
		} else {
			b.WriteString("        - その日はシフト登録なし\n")
		}
		for _, ov := range v.overlaps {
			b.WriteString("      - 時間重複\n")
			b.WriteString(fmt.Sprintf("        - %s（%s〜%s）\n", formatDurationJa(ov.end.Sub(ov.start)), ov.start.Format("15:04"), ov.end.Format("15:04")))
		}

		camFirst, camLast, camCount, err := firstLastNotification(ctx, cameraClient, cameraChannelID, cameraUserID, day, day.AddDate(0, 0, 1))
		if err != nil {
			return "", fmt.Errorf("get camera notifications: %w", err)
		}
		if camCount == 0 {
			continue
		}
		camFirst = camFirst.In(cameraNotifyJST)
		camLast = camLast.In(cameraNotifyJST)
		b.WriteString(fmt.Sprintf("      - 防犯センサー %s〜%s\n", camFirst.Format("15:04"), camLast.Format("15:04")))
		if startDiff := camFirst.Sub(v.start); startDiff > 0 {
			b.WriteString(fmt.Sprintf("        - 開始%s遅い\n", formatDurationJa(startDiff)))
		} else if startDiff < 0 {
			b.WriteString(fmt.Sprintf("        - 開始%s早い\n", formatDurationJa(-startDiff)))
		}
		if endDiff := camLast.Sub(v.end); endDiff > 0 {
			b.WriteString(fmt.Sprintf("        - 終了%s遅い\n", formatDurationJa(endDiff)))
		} else if endDiff < 0 {
			b.WriteString(fmt.Sprintf("        - 終了%s早い\n", formatDurationJa(-endDiff)))
		}
	}
	return b.String(), nil
}

// formatDurationJa は経過時間を"1時間30分"のような日本語表記にします。
func formatDurationJa(d time.Duration) string {
	minutes := int(d.Round(time.Minute).Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d分", minutes)
	}
	h, m := minutes/60, minutes%60
	if m == 0 {
		return fmt.Sprintf("%d時間", h)
	}
	return fmt.Sprintf("%d時間%d分", h, m)
}
