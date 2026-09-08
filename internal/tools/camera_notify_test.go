package tools

import (
	"context"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
)

// stubCameraNotifyClient はページングを含めてGetConversationHistoryContextの呼び出しを模擬します。
type stubCameraNotifyClient struct {
	pages [][]slackapi.Message
}

func (s *stubCameraNotifyClient) GetConversationHistoryContext(ctx context.Context, params *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error) {
	page := 0
	if params.Cursor != "" {
		page = int(params.Cursor[0] - '0')
	}
	resp := &slackapi.GetConversationHistoryResponse{
		Messages: s.pages[page],
		HasMore:  page+1 < len(s.pages),
	}
	if resp.HasMore {
		resp.ResponseMetaData.NextCursor = string(rune('0' + page + 1))
	}
	return resp, nil
}

func msg(user, ts string) slackapi.Message {
	m := slackapi.Message{}
	m.User = user
	m.Timestamp = ts
	return m
}

func TestFirstLastNotificationFiltersUserAndPaginates(t *testing.T) {
	client := &stubCameraNotifyClient{
		pages: [][]slackapi.Message{
			{msg("OTHER", "1788831437.751239"), msg("U09SXR64ZRQ", "1788664558.157079")},
			{msg("U09SXR64ZRQ", "1788831297.802929")},
		},
	}
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, cameraNotifyJST)
	end := start.AddDate(0, 0, 1)

	first, last, count, err := firstLastNotification(context.Background(), client, "C0BT3K6FTT3", "U09SXR64ZRQ", start, end)
	if err != nil {
		t.Fatalf("firstLastNotification() error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if got := first.Unix(); got != 1788664558 {
		t.Errorf("first.Unix() = %d, want 1788664558", got)
	}
	if got := last.Unix(); got != 1788831297 {
		t.Errorf("last.Unix() = %d, want 1788831297", got)
	}
}

func TestFirstLastNotificationNoMatch(t *testing.T) {
	client := &stubCameraNotifyClient{
		pages: [][]slackapi.Message{{msg("OTHER", "1788831437.751239")}},
	}
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, cameraNotifyJST)
	end := start.AddDate(0, 0, 1)

	_, _, count, err := firstLastNotification(context.Background(), client, "C0BT3K6FTT3", "U09SXR64ZRQ", start, end)
	if err != nil {
		t.Fatalf("firstLastNotification() error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}
