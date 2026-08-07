package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/yoshida-rio/marina/internal/proxyreply"
)

// Responder はユーザー発話に対する応答を生成するインターフェースです。internal/agent.Agentが実装します。
type Responder interface {
	Respond(ctx context.Context, threadKey, channel, user, userText string) (string, error)
}

// ProxyReplier は対象ユーザー宛のメッセージに対する代理返信フローを処理します。
// internal/proxyreply.Serviceが実装します。
type ProxyReplier interface {
	TargetUserID() string
	Handles(msg proxyreply.IncomingMessage) bool
	HandleMessage(ctx context.Context, msg proxyreply.IncomingMessage) error
	HandleAction(ctx context.Context, a proxyreply.Action) error
}

// EventAuthorization はイベントがどのトークンに対して配信されたかを表します。
// 本人のDMはUser OAuthトークン(xoxp-)の購読で届くため、Bot宛に届いたDMイベントと
// 区別する必要がありますが、slackevents.EventsAPIEventはauthorizationsを保持しないため
// 生のペイロードから取り出します。
type EventAuthorization struct {
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

// ParseEventAuthorizations はEvents APIのペイロード(外側のエンベロープ)からauthorizationsを取り出します。
// 取り出せない場合はnilを返します。
func ParseEventAuthorizations(payload []byte) []EventAuthorization {
	var envelope struct {
		Authorizations []EventAuthorization `json:"authorizations"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		log.Printf("slack: parse authorizations failed: %v", err)
		return nil
	}
	return envelope.Authorizations
}

// authorizedForUser はイベントが指定ユーザー本人のトークンに対して配信されたかを返します。
func authorizedForUser(auths []EventAuthorization, userID string) bool {
	for _, a := range auths {
		if !a.IsBot && a.UserID == userID {
			return true
		}
	}
	return false
}

// Handler はSlack Events APIからのHTTPリクエストを処理します。
type Handler struct {
	signingSecret string
	slackClient   *slack.Client
	agent         Responder
	proxy         ProxyReplier
	botUserID     string

	// targetDMChannelID はmarinaと代理返信対象ユーザーのDMチャンネルID(遅延解決してキャッシュ)。
	dmMu              sync.Mutex
	targetDMChannelID string
}

// NewHandler はHandlerを構築します。botTokenはSlack Web API呼び出し(chat.postMessage等)に使われます。
// proxyがnilでない場合、対象ユーザー宛のメンションは代理返信フロー(確認DM)に回されます。
func NewHandler(botToken, signingSecret string, agent Responder, proxy ProxyReplier) *Handler {
	client := slack.New(botToken)
	h := &Handler{
		signingSecret: signingSecret,
		slackClient:   client,
		agent:         agent,
		proxy:         proxy,
	}
	if auth, err := client.AuthTest(); err == nil {
		h.botUserID = auth.UserID
	} else {
		log.Printf("slack auth test failed (bot user id unknown): %v", err)
	}
	return h
}

// ServeHTTP はSlack Events APIのWebhookエンドポイントとして機能します。
// url_verificationチャレンジへの応答、署名検証、message/app_mentionイベントの非同期処理を行います。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := verifyRequest(r, h.signingSecret)
	if err != nil {
		http.Error(w, err.Error(), verifyErrorStatus(err))
		return
	}

	event, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		http.Error(w, "failed to parse event", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case slackevents.URLVerification:
		var r struct {
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			http.Error(w, "failed to parse challenge", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(r.Challenge))
		return

	case slackevents.CallbackEvent:
		// Slackの3秒タイムアウトに引っかからないよう、先に200 OKを返し実処理は非同期で行う。
		w.WriteHeader(http.StatusOK)
		go h.HandleEvent(event, ParseEventAuthorizations(body))
		return

	default:
		w.WriteHeader(http.StatusOK)
	}
}

// HandleEvent はパース済みのEvents APIイベントを処理します。
// HTTP(Events API Webhook)とSocket Modeの両方から呼ばれます。
// authsはイベントの配信先(ParseEventAuthorizationsの結果)で、DMの扱いを判定するのに使います。
func (h *Handler) HandleEvent(event slackevents.EventsAPIEvent, auths []EventAuthorization) {
	ctx := context.Background()

	var channel, user, text, threadTS, eventTS string

	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if inner.BotID != "" {
			return
		}
		channel, user, text = inner.Channel, inner.User, inner.Text
		threadTS, eventTS = inner.ThreadTimeStamp, inner.EventTimeStamp
	case *slackevents.MessageEvent:
		if inner.BotID != "" || inner.SubType != "" {
			return
		}
		if h.botUserID != "" && inner.User == h.botUserID {
			return
		}
		channel, user, text = inner.Channel, inner.User, inner.Text
		threadTS, eventTS = inner.ThreadTimeStamp, inner.TimeStamp

		// 対象ユーザー宛のメッセージは、marina自身が応答する代わりに代理返信フローに回す。
		// marinaもメンションされている場合はapp_mentionイベント側で通常応答するため、ここでは扱わない。
		msg := proxyreply.IncomingMessage{
			Channel:   channel,
			User:      user,
			Text:      text,
			MessageTS: inner.TimeStamp,
			ThreadTS:  inner.ThreadTimeStamp,
			IsDM:      inner.ChannelType == "im",
		}
		if h.shouldProxy(msg, inner.ChannelType, auths) {
			if err := h.proxy.HandleMessage(ctx, msg); err != nil {
				log.Printf("proxy reply error: %v", err)
			}
			return
		}
		// 代理返信の対象外だった本人のDM(本人が第三者に送ったDM等)には、
		// marinaが通常応答で割り込まないようここで打ち切る。
		if h.isTargetPrivateDM(msg, auths) {
			return
		}
	default:
		return
	}

	replyThreadTS := threadTS
	if replyThreadTS == "" {
		replyThreadTS = eventTS
	}
	threadKey := fmt.Sprintf("%s:%s", channel, replyThreadTS)

	reply, err := h.agent.Respond(ctx, threadKey, channel, user, text)
	if err != nil {
		log.Printf("agent respond error: %v", err)
		reply = "すみません、応答の生成中にエラーが発生しました。"
	}

	if _, _, err := h.slackClient.PostMessage(channel,
		slack.MsgOptionText(reply, false),
		slack.MsgOptionTS(replyThreadTS),
	); err != nil {
		log.Printf("slack post message error: %v", err)
	}
}

// shouldProxy は代理返信フローで処理すべきメッセージかを判定します。
// グループDM(mpim)は対象外、marina自身がメンションされている場合も通常応答に任せます。
// DMは「本人のUser OAuthトークンに対して配信されたイベント」に限ります。
// これがないと、第三者がmarinaへ送ったDM(Botトークンに配信される)まで
// 本人の代理返信対象になってしまいます。
func (h *Handler) shouldProxy(msg proxyreply.IncomingMessage, channelType string, auths []EventAuthorization) bool {
	if h.proxy == nil {
		return false
	}
	if channelType == "mpim" {
		return false
	}
	if h.botUserID != "" && strings.Contains(msg.Text, "<@"+h.botUserID+">") {
		return false
	}
	if !h.proxy.Handles(msg) {
		return false
	}
	if msg.IsDM && !authorizedForUser(auths, h.proxy.TargetUserID()) {
		// 本人のDMではない(marina宛のDM等)、またはauthorizationsが取得できていないケース。
		// 本人のDMを代理返信の対象にするにはUser OAuthトークンに im:history スコープが必要です。
		log.Printf("slack: skipping dm proxy reply in %s (event is not authorized for target user)", msg.Channel)
		return false
	}
	return true
}

// isTargetPrivateDM は「本人のUser OAuthトークンに配信された、marina以外とのDM」かを判定します。
// 本人が第三者とやりとりしているDMにmarinaが通常応答で割り込むのを防ぐために使います。
func (h *Handler) isTargetPrivateDM(msg proxyreply.IncomingMessage, auths []EventAuthorization) bool {
	if !msg.IsDM || h.proxy == nil {
		return false
	}
	targetUserID := h.proxy.TargetUserID()
	if !authorizedForUser(auths, targetUserID) {
		return false
	}
	// marinaと本人のDMは、marinaへの依頼を受け付ける通常の会話なので除外する。
	return msg.Channel != h.targetDMChannel(targetUserID)
}

// targetDMChannel はmarinaと対象ユーザーのDMチャンネルIDを返します。
// 初回のみconversations.openで解決し、以降はキャッシュを使います。
func (h *Handler) targetDMChannel(targetUserID string) string {
	if targetUserID == "" {
		return ""
	}
	h.dmMu.Lock()
	defer h.dmMu.Unlock()
	if h.targetDMChannelID != "" {
		return h.targetDMChannelID
	}
	channel, _, _, err := h.slackClient.OpenConversation(&slack.OpenConversationParameters{
		Users: []string{targetUserID},
	})
	if err != nil {
		log.Printf("slack: resolve dm channel for %s failed: %v", targetUserID, err)
		return ""
	}
	h.targetDMChannelID = channel.ID
	return h.targetDMChannelID
}

// verifyError は署名検証・リクエスト読み取りの失敗を表します。
type verifyError struct {
	message string
	status  int
}

func (e *verifyError) Error() string { return e.message }

func verifyErrorStatus(err error) int {
	if ve, ok := err.(*verifyError); ok {
		return ve.status
	}
	return http.StatusBadRequest
}

// HandleEventPayload はEvents APIの生ペイロードを同期的に処理します。
// Lambdaではハンドラのreturn後にgoroutineが凍結されるため、ワーカーLambda側から
// この関数で処理を完了させます(署名検証は受信側のLambdaで済ませてから呼び出します)。
func (h *Handler) HandleEventPayload(payload []byte) error {
	event, err := slackevents.ParseEvent(json.RawMessage(payload), slackevents.OptionNoVerifyToken())
	if err != nil {
		return fmt.Errorf("parse event: %w", err)
	}
	if event.Type != slackevents.CallbackEvent {
		return nil
	}
	h.HandleEvent(event, ParseEventAuthorizations(payload))
	return nil
}

// VerifySignature はSlackの署名ヘッダーを検証します。受信したボディをそのまま渡してください。
func VerifySignature(header http.Header, body []byte, signingSecret string) error {
	verifier, err := slack.NewSecretsVerifier(header, signingSecret)
	if err != nil {
		return fmt.Errorf("create verifier: %w", err)
	}
	if _, err := verifier.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := verifier.Ensure(); err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}
	return nil
}

// verifyRequest はSlackからのリクエストの署名を検証し、リクエストボディを返します。
func verifyRequest(r *http.Request, signingSecret string) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, &verifyError{message: "failed to read body", status: http.StatusBadRequest}
	}

	verifier, err := slack.NewSecretsVerifier(r.Header, signingSecret)
	if err != nil {
		return nil, &verifyError{message: "failed to create verifier", status: http.StatusBadRequest}
	}
	if _, err := verifier.Write(body); err != nil {
		return nil, &verifyError{message: "failed to verify", status: http.StatusBadRequest}
	}
	if err := verifier.Ensure(); err != nil {
		return nil, &verifyError{message: "invalid signature", status: http.StatusUnauthorized}
	}
	return body, nil
}
