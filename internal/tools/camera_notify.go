package tools

import (
	"context"
	"fmt"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/toolrunner"
	slackapi "github.com/slack-go/slack"
)

// cameraNotifyJST は通知時刻をJSTで表示するためのタイムゾーンです。
var cameraNotifyJST = time.FixedZone("JST", 9*60*60)

// CameraNotifyHistoryClient は監視カメラ通知チャンネルの履歴取得に使うSlack Web APIの操作です。
// 対象チャンネルはプライベートチャンネルのため、groups:historyスコープを持つUser OAuthトークンの
// クライアントを渡す想定です(Botトークンではmissing_scopeになります)。
type CameraNotifyHistoryClient interface {
	GetConversationHistoryContext(ctx context.Context, params *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
}

type cameraNotifyRangeInput struct {
	Date string `json:"date"`
}

// NewCameraNotifyTools は監視カメラ通知チャンネルで、指定日にuserIDから届いた通知の
// 最初/最後の時刻を調べるBetaToolセットを構築します。
func NewCameraNotifyTools(client CameraNotifyHistoryClient, channelID, userID string) ([]anthropic.BetaTool, error) {
	tool, err := toolrunner.NewBetaToolFromBytes[cameraNotifyRangeInput](
		"get_camera_notification_time_range",
		"監視カメラ通知チャンネルに、指定した日付(JST)で最初と最後に届いた通知の時刻を調べる。dateはYYYY-MM-DD形式(JST)で渡す。",
		mustSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"date": map[string]any{"type": "string", "description": "調べたい日付(YYYY-MM-DD、JST)"},
			},
			"required": []string{"date"},
		}),
		func(ctx context.Context, in cameraNotifyRangeInput) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			day, err := time.ParseInLocation("2006-01-02", in.Date, cameraNotifyJST)
			if err != nil {
				return textResult(fmt.Sprintf("dateの形式が正しくありません: %v", err)), nil
			}

			first, last, count, err := firstLastNotification(ctx, client, channelID, userID, day, day.AddDate(0, 0, 1))
			if err != nil {
				return textResult(""), err
			}
			if count == 0 {
				return textResult(fmt.Sprintf("%sの通知は見つかりませんでした。", in.Date)), nil
			}
			return textResult(fmt.Sprintf("%sの通知(%d件): 最初は%s、最後は%s。",
				in.Date, count,
				first.In(cameraNotifyJST).Format("15:04:05"),
				last.In(cameraNotifyJST).Format("15:04:05"))), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []anthropic.BetaTool{tool}, nil
}

// firstLastNotification はstart(含む)からend(含まない)の間にuserIDから届いたメッセージの
// 最初/最後の投稿時刻を返します。該当メッセージが無い場合はcount=0です。
func firstLastNotification(ctx context.Context, client CameraNotifyHistoryClient, channelID, userID string, start, end time.Time) (first, last time.Time, count int, err error) {
	oldest := fmt.Sprintf("%d.000000", start.Unix())
	// Slackのlatestは指定時刻"より前"のメッセージのみを含むため、日境界のendをそのまま渡す。
	latest := fmt.Sprintf("%d.000000", end.Unix())

	cursor := ""
	for {
		resp, err := client.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
			ChannelID: channelID,
			Oldest:    oldest,
			Latest:    latest,
			Inclusive: true,
			Limit:     200,
			Cursor:    cursor,
		})
		if err != nil {
			return time.Time{}, time.Time{}, 0, fmt.Errorf("get conversation history: %w", err)
		}
		for _, m := range resp.Messages {
			if m.User != userID {
				continue
			}
			ts, err := parseSlackTimestamp(m.Timestamp)
			if err != nil {
				continue
			}
			if count == 0 || ts.Before(first) {
				first = ts
			}
			if count == 0 || ts.After(last) {
				last = ts
			}
			count++
		}
		if !resp.HasMore || resp.ResponseMetaData.NextCursor == "" {
			break
		}
		cursor = resp.ResponseMetaData.NextCursor
	}
	return first, last, count, nil
}

// parseSlackTimestamp はSlackのts("1700000000.123456")形式をtime.Timeへ変換します。
func parseSlackTimestamp(ts string) (time.Time, error) {
	var sec, micro int64
	if _, err := fmt.Sscanf(ts, "%d.%d", &sec, &micro); err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, micro*1000), nil
}
