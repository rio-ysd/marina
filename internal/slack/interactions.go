package slack

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/proxyreply"
)

// InteractionHandler はSlackのInteractivity(Block Kitのボタン押下)のWebhookを処理します。
// 代理返信の確認DMに付いたYes/Noボタンの押下がここに届きます。
type InteractionHandler struct {
	signingSecret string
	proxy         ProxyReplier
}

func NewInteractionHandler(signingSecret string, proxy ProxyReplier) *InteractionHandler {
	return &InteractionHandler{signingSecret: signingSecret, proxy: proxy}
}

// ServeHTTP はInteractivity Request URL(例: /slack/interactions)として機能します。
// Slackは3秒以内の応答を求めるため、先に200 OKを返して実処理は非同期で行います。
func (h *InteractionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := verifyRequest(r, h.signingSecret)
	if err != nil {
		http.Error(w, err.Error(), verifyErrorStatus(err))
		return
	}

	// Interactivityのペイロードはapplication/x-www-form-urlencodedのpayloadフィールドで届く。
	values, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}
	payload := values.Get("payload")
	if payload == "" {
		http.Error(w, "payload is empty", http.StatusBadRequest)
		return
	}

	var callback slack.InteractionCallback
	if err := json.Unmarshal([]byte(payload), &callback); err != nil {
		http.Error(w, "failed to parse payload", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	go h.HandleInteraction(callback)
}

// HandleInteraction はパース済みのInteractivityペイロードを処理します。
// HTTP(Interactivity Request URL)とSocket Modeの両方から呼ばれます。
func (h *InteractionHandler) HandleInteraction(callback slack.InteractionCallback) {
	if h.proxy == nil || callback.Type != slack.InteractionTypeBlockActions {
		return
	}

	channel := callback.Container.ChannelID
	if channel == "" {
		channel = callback.Channel.ID
	}
	ts := callback.Container.MessageTs
	if ts == "" {
		ts = callback.Message.Timestamp
	}

	ctx := context.Background()
	for _, action := range callback.ActionCallback.BlockActions {
		if action == nil {
			continue
		}
		if action.ActionID != proxyreply.ActionApprove && action.ActionID != proxyreply.ActionReject {
			continue
		}
		if err := h.proxy.HandleAction(ctx, proxyreply.Action{
			ActionID:        action.ActionID,
			Value:           action.Value,
			ActorUserID:     callback.User.ID,
			ApprovalChannel: channel,
			ApprovalTS:      ts,
		}); err != nil {
			log.Printf("proxy reply action error: %v", err)
		}
	}
}
