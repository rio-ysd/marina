package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/slack-go/slack"

	"github.com/yoshida-rio/marina/internal/proxyreply"
	"github.com/yoshida-rio/marina/internal/userprovision"
)

// UserCreationApprover はアカウント作成の確認DMに付いたYes/Noボタンの押下を処理します。
// internal/userprovision.Serviceが実装します。
type UserCreationApprover interface {
	HandleAction(ctx context.Context, a userprovision.Action) error
}

// InteractionHandler はSlackのInteractivity(Block Kitのボタン押下)のWebhookを処理します。
// 代理返信とアカウント作成の確認DMに付いたYes/Noボタンの押下がここに届きます。
type InteractionHandler struct {
	signingSecret string
	proxy         ProxyReplier
	userCreation  UserCreationApprover
}

// NewInteractionHandler はInteractionHandlerを構築します。
// proxy / userCreation はそれぞれの機能が無効な場合nilで渡され、その種類のボタンは無視されます。
func NewInteractionHandler(signingSecret string, proxy ProxyReplier, userCreation UserCreationApprover) *InteractionHandler {
	return &InteractionHandler{signingSecret: signingSecret, proxy: proxy, userCreation: userCreation}
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

// HandleInteractionPayload はInteractivityの生リクエストボディ(application/x-www-form-urlencoded)を
// 同期的に処理します。Lambdaではハンドラのreturn後にgoroutineが凍結されるため、
// ワーカーLambda側からこの関数で処理を完了させます(署名検証は受信側で済ませてから呼び出します)。
func (h *InteractionHandler) HandleInteractionPayload(body []byte) error {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return fmt.Errorf("parse form: %w", err)
	}
	payload := values.Get("payload")
	if payload == "" {
		return fmt.Errorf("payload is empty")
	}
	var callback slack.InteractionCallback
	if err := json.Unmarshal([]byte(payload), &callback); err != nil {
		return fmt.Errorf("parse payload: %w", err)
	}
	h.HandleInteraction(callback)
	return nil
}

// HandleInteraction はパース済みのInteractivityペイロードを処理します。
// HTTP(Interactivity Request URL)とSocket Modeの両方から呼ばれます。
func (h *InteractionHandler) HandleInteraction(callback slack.InteractionCallback) {
	if callback.Type != slack.InteractionTypeBlockActions {
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
		switch action.ActionID {
		case proxyreply.ActionApprove, proxyreply.ActionReject:
			if h.proxy == nil {
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
		case userprovision.ActionApprove, userprovision.ActionReject:
			if h.userCreation == nil {
				continue
			}
			if err := h.userCreation.HandleAction(ctx, userprovision.Action{
				ActionID:        action.ActionID,
				Value:           action.Value,
				ActorUserID:     callback.User.ID,
				ApprovalChannel: channel,
				ApprovalTS:      ts,
			}); err != nil {
				log.Printf("user creation action error: %v", err)
			}
		}
	}
}
