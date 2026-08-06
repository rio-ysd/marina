// cmd/socketmode はSocket Mode(WebSocket)でSlackイベントとInteractivity(ボタン押下)を受信する
// 常駐エントリポイントです。公開URL(Request URL)が不要なため、ローカル開発でngrokを立てずに
// 代理返信のYes/Noボタンまで動作確認できます。
// WebSocketを張り続ける必要があるためLambdaでは使えません(本番はcmd/lambdaのHTTP受信を使います)。
package main

import (
	"log"
	"strings"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
	marinaslack "github.com/yoshida-rio/marina/internal/slack"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.SlackAppToken == "" {
		log.Fatalf("SLACK_APP_TOKEN is not set (Socket Modeにはconnections:writeスコープのApp-Level Token(xapp-)が必要です)")
	}
	if !strings.HasPrefix(cfg.SlackAppToken, "xapp-") {
		log.Fatalf("SLACK_APP_TOKEN must start with xapp-")
	}

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer a.DB.Close()

	api := slackapi.New(cfg.SlackBotToken, slackapi.OptionAppLevelToken(cfg.SlackAppToken))
	client := socketmode.New(api)

	go func() {
		for evt := range client.Events {
			switch evt.Type {
			case socketmode.EventTypeConnecting:
				log.Println("connecting to slack via socket mode...")
			case socketmode.EventTypeConnected:
				log.Println("connected to slack via socket mode")
			case socketmode.EventTypeInvalidAuth:
				log.Println("socket mode auth failed: check SLACK_APP_TOKEN")
			case socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError,
				socketmode.EventTypeErrorBadMessage, socketmode.EventTypeErrorWriteFailed:
				log.Printf("socket mode error: %+v", evt.Data)

			case socketmode.EventTypeEventsAPI:
				event, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					log.Printf("unexpected events api payload: %T", evt.Data)
					continue
				}
				// Slackの3秒タイムアウトに引っかからないよう、先にAckを返し実処理は非同期で行う。
				ack(client, evt)
				// authorizations(イベントの配信先)は生ペイロードにしか含まれないため、ここで取り出して渡す。
				var auths []marinaslack.EventAuthorization
				if evt.Request != nil {
					auths = marinaslack.ParseEventAuthorizations(evt.Request.Payload)
				}
				go a.SlackHandler.HandleEvent(event, auths)

			case socketmode.EventTypeInteractive:
				callback, ok := evt.Data.(slackapi.InteractionCallback)
				if !ok {
					log.Printf("unexpected interactive payload: %T", evt.Data)
					continue
				}
				ack(client, evt)
				go a.InteractionHandler.HandleInteraction(callback)
			}
		}
	}()

	log.Println("marina socket mode client starting")
	if err := client.Run(); err != nil {
		log.Fatalf("socket mode run: %v", err)
	}
}

func ack(client *socketmode.Client, evt socketmode.Event) {
	if evt.Request == nil {
		return
	}
	if err := client.Ack(*evt.Request); err != nil {
		log.Printf("socket mode ack error: %v", err)
	}
}
