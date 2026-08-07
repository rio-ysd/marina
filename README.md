# marina

Slack上で動く秘書AIエージェント。Claude (Anthropicモデル) をAmazon Bedrock経由で呼び出し、
タスク管理・リマインダー・雑務相談に応答し、Gmailの朝チェックやMoneyForwardクラウド会計の見積書/請求書作成を
Function Callingツールとして提供します。

## 構成

- `cmd/server` — ローカル開発用HTTPサーバー (Slack Events APIを受信)
- `cmd/lambda` — AWS Lambda用エントリポイント (API Gateway経由でSlack Events APIを受信)
- `cmd/morningdigest` — 毎朝9時のGmailダイジェスト用Lambdaエントリポイント (EventBridge Schedulerから起動)
- `cmd/reminderworker` — 未送信リマインダーをSlackへ送信するLambdaエントリポイント (EventBridge Schedulerから数分おきに起動)
- `cmd/eventworker` — Slackイベント/ボタン押下の実処理を行うLambdaエントリポイント (`cmd/lambda`から非同期invoke)
- `cmd/socketmode` — Socket Mode(WebSocket)でイベント/ボタン押下を受信する常駐エントリポイント (公開URL不要、Lambda不可)
- `infra` — AWS CDK(TypeScript)によるインフラ定義
- `internal/agent` — Claude(Tool Useループ)
- `internal/bedrockclient` — Amazon Bedrock経由でAnthropicクライアントを構築
- `internal/tools` — タスク/リマインダー/Gmail/MoneyForward請求書のFunction Calling定義
- `internal/slack` — Slack Events API受信・署名検証・応答送信、Interactivity(ボタン押下)受信
- `internal/proxyreply` — 本人宛メンションへの返信案作成→DMでYes/No確認→本人として代理返信
- `internal/mfoauth` — MoneyForwardクラウド請求書API用OAuth2認可コールバック(`/mf/oauth/start`, `/mf/oauth/callback`)
- `internal/storage` — MySQL(RDS)への永続化
- `internal/morningdigest` — 朝のGmailダイジェストのロジック
- `migrations` — golang-migrate用SQLマイグレーション

Gmail連携はGoogle Workspaceサービスアカウント + ドメイン委任(`GmailClient`)、MoneyForward請求書連携は
OAuth2 authorizationCodeフロー(`MFInvoiceClient`)で実装しています。いずれも認証情報未設定時はモック実装で動作します。

MoneyForward請求書APIはOAuth2のみをサポートしており、APIキー方式は使えません。初回のみ人間がブラウザで
`https://<公開ドメイン>/mf/oauth/start` にアクセスして認可する必要がありますが、以降はrefresh_tokenが
`oauth_tokens`テーブルに保存され自動更新されるため、Slackからの利用時に人間の介入は不要です。
見積書・請求書を作成する前に、Claudeが`mf_search_partners`ツールで取引先のdepartment_idを確認してから
`mf_create_estimate`/`mf_create_invoice`を呼び出します。

## 代理返信フロー(本人宛メンションのYes/No確認付き自動返信)

対象ユーザー(`PROXY_REPLY_TARGET_USER_ID`)宛にメッセージが届くと、marinaが返信案を作成して本人にDMで
「この内容で返信しますか?」とYes/Noボタン付きで確認します。Yesを押すとUser OAuthトークン(`xoxp-`)を使って
**本人の発言として**元のスレッドへ投稿します。対象はチャンネルでの本人宛メンションと、本人へのDMの2種類です。

- 有効化には `SLACK_USER_OAUTH_TOKEN` (User OAuthトークン、`chat:write`) と `PROXY_REPLY_TARGET_USER_ID` の両方が必要
- `PROXY_REPLY_CHANNEL_IDS` で対象チャンネルを絞れます(空ならBotが参加している全チャンネル。DMには影響しません)
- **DMを対象にするにはUser Token Scopeに `im:history` が必要**です。marinaが本人の全DMを受信する構成になるため、
  不要なら `PROXY_REPLY_INCLUDE_DM=false` で無効化できます
- Slack Appで **Interactivity** を有効化してください。HTTP受信の場合はRequest URLに
  `https://<公開ドメイン>/slack/interactions` を設定します(Socket ModeではRequest URLは不要)

詳細は [docs/proxy-reply.md](docs/proxy-reply.md) を参照してください。

## Claude / Amazon Bedrockの設定

marinaはClaudeをAnthropic APIに直接接続せず、Amazon Bedrock経由で呼び出します。

- `ANTHROPIC_MODEL` — 呼び出すモデルID。Bedrockでは `anthropic.` プレフィックス付き(例: `anthropic.claude-sonnet-5`)
- `BEDROCK_REGION` — Bedrockのリージョン。既定値は `us-east-1`
- 認証は標準のAWSクレデンシャルチェーン(SigV4)を使用します。Lambda実行ロールに `bedrock:InvokeModel` /
  `bedrock:InvokeModelWithResponseStream` 権限を付与してください。ローカル実行時は `AWS_ACCESS_KEY_ID` /
  `AWS_SECRET_ACCESS_KEY` (必要なら `AWS_SESSION_TOKEN`) を環境変数で設定するか、`~/.aws/credentials` の
  プロファイルを使用します
- 使用するモデルはBedrockのModel Access(モデルアクセス)で事前に有効化しておく必要があります

## ローカル環境での起動

1. `.env.example` を `.env` にコピーし、`SLACK_BOT_TOKEN` / `SLACK_SIGNING_SECRET` / `AWS_ACCESS_KEY_ID` /
   `AWS_SECRET_ACCESS_KEY` を設定する
2. Docker Composeで起動する

```sh
cp .env.example .env
docker compose --env-file .env up --build
```

- `mysql` コンテナ起動後、`migrate` コンテナが `migrations/` のマイグレーションを自動適用します
- `app` コンテナがポート8080でSlack Events APIエンドポイント (`/slack/events`) を提供します
- SlackからのWebhookをローカルで受けるには `ngrok http 8080` 等でトンネルし、Slack AppのEvent Subscriptions URLに
  `https://<ngrok-domain>/slack/events` を設定してください

### Socket Modeで動かす(ngrok不要)

Socket Modeを使うと公開URL(Request URL)が不要になります。Slack AppでSocket Modeを有効化し、
App-Level Token(`xapp-`、スコープ `connections:write`)を `.env` の `SLACK_APP_TOKEN` に設定してから起動します。

```sh
docker compose --env-file .env --profile socketmode up --build socketmode
```

Socket ModeはWebSocketを張り続ける常駐プロセスのため、Lambdaでは利用できません(本番はHTTP受信のまま)。

## Slack App側の設定

- Bot Token Scopes: `chat:write`, `app_mentions:read`, `channels:history`, `im:history` など、対話に必要なスコープ
  (代理返信フローを使う場合はさらに `im:write`, `users:read`, `groups:history`)
- User Token Scopes: 代理返信フローを使う場合は `chat:write` (本人として投稿するため)、
  DMも対象にする場合は `im:history` も追加
- Event Subscriptions: `message.channels`, `message.im`, `app_mention` を有効化
- Interactivity & Shortcuts: 代理返信のYes/Noボタンを使う場合は有効化し、Request URLに
  `https://<公開ドメイン>/slack/interactions` を設定
- Signing Secret / Bot User OAuth Token を `.env` の `SLACK_SIGNING_SECRET` / `SLACK_BOT_TOKEN` に設定
- User OAuth Token (`xoxp-`) を `.env` の `SLACK_USER_OAUTH_TOKEN` に設定

## 本番(AWS)構成

- API Gateway (HTTP API) → `cmd/lambda` : Slack Events API / Interactivity受信(署名検証とackのみ)
- `cmd/lambda` → `cmd/eventworker` を非同期invoke : Claude呼び出しとSlackへの投稿
- EventBridge (平日9:00 JST) → `cmd/morningdigest` : Gmail朝チェック
- EventBridge (5分おき) → `cmd/reminderworker` : リマインダー送信
- RDS (MySQL) : タスク/リマインダー/会話履歴/OAuthトークン/返信下書きの永続化
- Secrets Manager : Slackトークン等の設定値とDB認証情報

インフラは [infra/](infra/) の AWS CDK で定義し、`main`へのpushで GitHub Actions がデプロイします。
手順・費用・トラブルシューティングは [docs/deploy.md](docs/deploy.md) を参照してください。

## ビルド

```sh
go build ./...
go vet ./...
```
