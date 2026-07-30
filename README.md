# marina

Slack上で動く秘書AIエージェント。Claude (Anthropicモデル) をAmazon Bedrock経由で呼び出し、
タスク管理・リマインダー・雑務相談に応答し、Gmailの朝チェックやMoneyForwardクラウド会計の見積書/請求書作成を
Function Callingツールとして提供します。

## 構成

- `cmd/server` — ローカル開発用HTTPサーバー (Slack Events APIを受信)
- `cmd/lambda` — AWS Lambda用エントリポイント (API Gateway経由でSlack Events APIを受信)
- `cmd/morningdigest` — 毎朝9時のGmailダイジェスト用Lambdaエントリポイント (EventBridge Schedulerから起動)
- `cmd/reminderworker` — 未送信リマインダーをSlackへ送信するLambdaエントリポイント (EventBridge Schedulerから数分おきに起動)
- `internal/agent` — Claude(Tool Useループ)
- `internal/bedrockclient` — Amazon Bedrock経由でAnthropicクライアントを構築
- `internal/tools` — タスク/リマインダー/Gmail/MoneyForward請求書のFunction Calling定義
- `internal/slack` — Slack Events API受信・署名検証・応答送信
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

## Slack App側の設定

- Bot Token Scopes: `chat:write`, `app_mentions:read`, `channels:history`, `im:history` など、対話に必要なスコープ
- Event Subscriptions: `message.channels`, `message.im`, `app_mention` を有効化
- Signing Secret / Bot User OAuth Token を `.env` の `SLACK_SIGNING_SECRET` / `SLACK_BOT_TOKEN` に設定

## 本番(AWS)構成の想定

- API Gateway (HTTP API) → `cmd/lambda` : Slack Events API受信
- EventBridge Scheduler (平日9:00 JST) → `cmd/morningdigest` : Gmail朝チェック
- EventBridge Scheduler (数分おき) → `cmd/reminderworker` : リマインダー送信
- RDS (MySQL) : タスク/リマインダー/会話履歴/OAuthトークンの永続化

Lambda/API Gateway/EventBridge Scheduler/RDSのIaC構築は本リポジトリのスコープ外です。

## ビルド

```sh
go build ./...
go vet ./...
```
