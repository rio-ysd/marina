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

// InteractionHandler はSlackのInteractivity(Block Kitのボタン押下・モーダルの送信)のWebhookを処理します。
// 代理返信とアカウント作成の確認DMに付いたYes/Noボタン、App Homeの編集操作がここに届きます。
type InteractionHandler struct {
	signingSecret string
	proxy         ProxyReplier
	userCreation  UserCreationApprover
	home          *HomeHandler
}

// NewInteractionHandler はInteractionHandlerを構築します。
// proxy / userCreation はそれぞれの機能が無効な場合nilで渡され、その種類のボタンは無視されます。
func NewInteractionHandler(signingSecret string, proxy ProxyReplier, userCreation UserCreationApprover, home *HomeHandler) *InteractionHandler {
	return &InteractionHandler{signingSecret: signingSecret, proxy: proxy, userCreation: userCreation, home: home}
}

// ServeHTTP はInteractivity Request URL(例: /slack/interactions)として機能します。
// Slackは3秒以内の応答を求めるため、先に200 OKを返して実処理は非同期で行います。
func (h *InteractionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := verifyRequest(r, h.signingSecret)
	if err != nil {
		http.Error(w, err.Error(), verifyErrorStatus(err))
		return
	}

	callback, err := parseInteractionPayload(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	go h.HandleInteraction(callback)
}

// parseInteractionPayload はInteractivityのリクエストボディからペイロードを取り出します。
// ペイロードはapplication/x-www-form-urlencodedのpayloadフィールドで届きます。
func parseInteractionPayload(body []byte) (slack.InteractionCallback, error) {
	var callback slack.InteractionCallback
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return callback, fmt.Errorf("parse form: %w", err)
	}
	payload := values.Get("payload")
	if payload == "" {
		return callback, fmt.Errorf("payload is empty")
	}
	if err := json.Unmarshal([]byte(payload), &callback); err != nil {
		return callback, fmt.Errorf("parse payload: %w", err)
	}
	return callback, nil
}

// HandleInteractionPayload はInteractivityの生リクエストボディ(application/x-www-form-urlencoded)を
// 同期的に処理します。Lambdaではハンドラのreturn後にgoroutineが凍結されるため、
// ワーカーLambda側からこの関数で処理を完了させます(署名検証は受信側で済ませてから呼び出します)。
func (h *InteractionHandler) HandleInteractionPayload(body []byte) error {
	callback, err := parseInteractionPayload(body)
	if err != nil {
		return err
	}
	h.HandleInteraction(callback)
	return nil
}

// TryHandleSync はワーカーへ回さずその場で処理すべきInteractivityを処理し、処理したかを返します。
// App Homeのモーダルはtrigger_idの有効期限が3秒しかなく、ワーカーLambdaの起動を待つと
// expired_trigger_idで開けません。そのため受信Lambdaから直接呼び出します
// (views.open / views.publish はSlackのAPIを1回叩くだけなので3秒に収まります)。
// falseを返した場合は従来どおりワーカーへ回してください。
func (h *InteractionHandler) TryHandleSync(body []byte) (bool, error) {
	callback, err := parseInteractionPayload(body)
	if err != nil {
		return false, err
	}
	if !h.isHomeInteraction(callback) {
		return false, nil
	}
	h.HandleInteraction(callback)
	return true, nil
}

// isHomeInteraction はApp Home由来の操作かを返します。
func (h *InteractionHandler) isHomeInteraction(callback slack.InteractionCallback) bool {
	if h.home == nil {
		return false
	}
	if callback.Type == slack.InteractionTypeViewSubmission {
		return callback.View.CallbackID == CallbackEditInstructions
	}
	if callback.Type != slack.InteractionTypeBlockActions {
		return false
	}
	for _, action := range callback.ActionCallback.BlockActions {
		if action != nil && action.ActionID == ActionEditInstructions {
			return true
		}
	}
	return false
}

// HandleInteraction はパース済みのInteractivityペイロードを処理します。
// HTTP(Interactivity Request URL)とSocket Modeの両方から呼ばれます。
func (h *InteractionHandler) HandleInteraction(callback slack.InteractionCallback) {
	if callback.Type == slack.InteractionTypeViewSubmission {
		if h.home == nil || callback.View.CallbackID != CallbackEditInstructions {
			return
		}
		if err := h.home.SaveFromSubmission(context.Background(), callback); err != nil {
			log.Printf("home instructions save error: %v", err)
		}
		return
	}
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
		case ActionEditInstructions:
			if err := h.home.OpenEditModal(ctx, callback.TriggerID, callback.User.ID); err != nil {
				log.Printf("open home edit modal error: %v", err)
			}
		}
	}
}
