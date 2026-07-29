package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// Responder はユーザー発話に対する応答を生成するインターフェースです。internal/agent.Agentが実装します。
type Responder interface {
	Respond(ctx context.Context, threadKey, channel, user, userText string) (string, error)
}

// Handler はSlack Events APIからのHTTPリクエストを処理します。
type Handler struct {
	signingSecret string
	slackClient   *slack.Client
	agent         Responder
	botUserID     string
}

// NewHandler はHandlerを構築します。botTokenはSlack Web API呼び出し(chat.postMessage等)に使われます。
func NewHandler(botToken, signingSecret string, agent Responder) *Handler {
	client := slack.New(botToken)
	h := &Handler{
		signingSecret: signingSecret,
		slackClient:   client,
		agent:         agent,
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	verifier, err := slack.NewSecretsVerifier(r.Header, h.signingSecret)
	if err != nil {
		http.Error(w, "failed to create verifier", http.StatusBadRequest)
		return
	}
	if _, err := verifier.Write(body); err != nil {
		http.Error(w, "failed to verify", http.StatusBadRequest)
		return
	}
	if err := verifier.Ensure(); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
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
		go h.handleCallbackEvent(event)
		return

	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) handleCallbackEvent(event slackevents.EventsAPIEvent) {
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
