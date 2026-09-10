package tools

import (
	"context"
	"database/sql"
	"fmt"
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
			"beryx_nameに本名を渡す。month(YYYY-MM、省略時は現在月)を対象に、attendance-rules.mdの形式で月次勤怠報告を返す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":       map[string]any{"type": "string", "description": "dqlでのスタッフの名前(部分一致)"},
				"beryx_name": map[string]any{"type": "string", "description": "beryx_production側の本名(部分一致、nameと異なる場合のみ指定)"},
				"month":      map[string]any{"type": "string", "description": "調査対象月(YYYY-MM、省略時は現在月)"},
			},
			"required": []string{"name"},
		}),
		func(ctx context.Context, in dqlShiftComplianceInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			text, err := buildStaffAttendanceSummary(ctx, db, cameraClient, cameraChannelID, cameraUserID, in.Name, in.BeryxName, in.Month)
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

func buildStaffAttendanceSummary(ctx context.Context, db *sql.DB, cameraClient CameraNotifyHistoryClient, cameraChannelID, cameraUserID, name, beryxName, month string) (string, error) {
	if month == "" {
		month = currentJSTMonth()
	}
	return buildMonthlyStaffAttendanceSummary(ctx, db, cameraClient, cameraChannelID, cameraUserID, name, beryxName, month)
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
