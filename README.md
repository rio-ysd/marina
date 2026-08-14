# marina

Slack上で動く秘書AIエージェント。Claude (Anthropicモデル) をAmazon Bedrock経由で呼び出し、
タスク管理・リマインダー・雑務相談に応答し、Gmailの朝チェック、Googleカレンダーの予定確認、
Google Driveの資料検索、スプレッドシートの読み書き、MoneyForwardクラウド請求書の見積書/請求書作成を
Function Callingツールとして提供します。

外部サービス連携はすべてFunction Calling(Tool Use)で実装する方針です。実装の作法と新規連携の追加手順は
[docs/integration-guidelines.md](docs/integration-guidelines.md) にまとめてあります。

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
- `internal/tools` — タスク/リマインダー/Gmail/Googleカレンダー/Google Drive/スプレッドシート/MoneyForward請求書のFunction Calling定義
- `internal/slack` — Slack Events API受信・署名検証・応答送信、Interactivity(ボタン押下)受信
- `internal/proxyreply` — 本人宛メンションへの返信案作成→DMでYes/No確認→本人として代理返信
- `internal/mfoauth` — MoneyForwardクラウド請求書API用OAuth2認可コールバック(`/mf/oauth/start`, `/mf/oauth/callback`)
- `internal/instructions` — App Homeで編集する「追加の運用ルール」の読み書き
- `internal/storage` — MySQL(RDS)への永続化
- `internal/morningdigest` — 朝のGmailダイジェストのロジック
- `migrations` — golang-migrate用SQLマイグレーション

Google連携(Gmail / カレンダー / Drive / スプレッドシート)はGoogle Workspaceサービスアカウント +
ドメイン委任で実装しており、4つとも同じ認証情報(`GOOGLE_SERVICE_ACCOUNT_JSON` /
`GOOGLE_IMPERSONATED_USER`)を使います。MoneyForward請求書連携はOAuth2 authorizationCodeフロー
(`MFInvoiceClient`)です。いずれも認証情報未設定時はモック実装で動作します。

| 連携 | ツール | ドメイン委任に必要なスコープ |
|---|---|---|
| Gmail | `gmail_list_unread` / `gmail_mark_as_read` / `gmail_create_draft` | `gmail.readonly`, `gmail.modify`, `gmail.compose` |
| カレンダー | `calendar_list_events` / `calendar_create_event` | `calendar` |
| Drive | `drive_search_files` / `drive_list_folder` / `drive_read_file` / `drive_create_document` | `drive` |
| スプレッドシート | `sheets_get_info` / `sheets_read_range` / `sheets_append_rows` | `spreadsheets` |
| 社内メンバー・連絡先の検索 | `people_search_directory` / `people_search_contacts` | `directory.readonly`, `contacts.readonly` |
| アカウント作成(承認フロー付き) | `directory_request_user_creation` | `admin.directory.user` |

**ドメイン委任の編集画面は入力した内容で許可スコープが置き換わります**。どれか1つを足すときも、
上の表のスコープをすべて入力してください(`https://www.googleapis.com/auth/` を頭に付けたもの)。
加えて、**Google Cloudコンソールで各APIの有効化が必要**です(Drive / Calendar / Sheets / People /
Admin SDK)。有効化されていないAPIは、スコープを登録しても `403 SERVICE_DISABLED` で失敗します。

削除・共有権限の変更・既存データの上書き・予定の変更・参加者の招待は、Slackの曖昧な指示で誤爆すると
戻せないため意図的にツールとして公開していません。詳細は
[docs/google-drive.md](docs/google-drive.md) / [docs/google-calendar.md](docs/google-calendar.md) /
[docs/google-sheets.md](docs/google-sheets.md) / [docs/google-people.md](docs/google-people.md) を
参照してください。

アカウント作成だけは業務上必要なため、**承認者のYes/No確認を挟むフロー**として実装しています。
Claudeが呼べるのは依頼ツールのみで、作成を直接実行するツールは存在しません。
`USER_PROVISION_APPROVER_SLACK_USER_ID` が未設定の場合は依頼ツール自体を登録しません。
詳細は [docs/google-user-provisioning.md](docs/google-user-provisioning.md) を参照してください。

なお、Google+ API / Google+ Domains API はどちらも廃止済みで、そもそもアカウント発行の機能を
持っていませんでした。社内メンバーの検索はPeople API、アカウント作成はAdmin SDK Directory APIが
後継にあたります。

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

## 追加の運用ルール(App Homeから編集)

Slack App Homeのホームタブに「追加の運用ルール」を書いておくと、その内容が**Slackでの応答**と
**朝のメールダイジェスト**の両方のシステムプロンプトに差し込まれます。コード変更・再デプロイなしで
判断のルールを足せます(例: `SALON BOARD関連のメールは重要ではないので、既読にするだけでよい`)。

- 編集できるのは `HOME_ADMIN_SLACK_USER_ID` に指定したユーザーのみ。未設定なら閲覧のみになります
- Slack Appで **App Home の Home Tab を有効化**し、**`app_home_opened` イベントを購読**してください
- 保存先は `app_settings` テーブル(キー `custom_instructions`)。更新者と更新日時もホームタブに表示します
- 上限は3000文字。システムプロンプトは全リクエストに乗るため、長いほど毎回のトークン消費が増えます

詳細は [docs/app-home.md](docs/app-home.md) を参照してください。

MoneyForwardクラウド会計API連携は未実装です。調査結果と実装に必要な情報は
[docs/mf-accounting-api.md](docs/mf-accounting-api.md) にまとめてあります。

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
  (代理返信フローを使う場合はさらに `im:write`, `users:read`, `groups:history`。
  応答中のリアクション表示を使う場合は `reactions:write`)
- User Token Scopes: 代理返信フローを使う場合は `chat:write` (本人として投稿するため)、
  DMも対象にする場合は `im:history` も追加
- Event Subscriptions: `message.channels`, `message.im`, `app_mention` を有効化
  (App Homeの追加ルールを使う場合はさらに `app_home_opened`)
- App Home: 追加の運用ルールをホームタブから編集する場合は「Home Tab」を有効化
- Interactivity & Shortcuts: 代理返信のYes/Noボタン、またはApp Homeの編集を使う場合は有効化し、Request URLに
  `https://<公開ドメイン>/slack/interactions` を設定
- Signing Secret / Bot User OAuth Token を `.env` の `SLACK_SIGNING_SECRET` / `SLACK_BOT_TOKEN` に設定
- User OAuth Token (`xoxp-`) を `.env` の `SLACK_USER_OAUTH_TOKEN` に設定

## 本番(AWS)構成

- API Gateway (HTTP API) → `cmd/lambda` : Slack Events API / Interactivity受信(署名検証とackのみ)
- `cmd/lambda` → `cmd/eventworker` を非同期invoke : Claude呼び出しとSlackへの投稿
- EventBridge (平日9:00 JST) → `cmd/morningdigest` : Gmail朝チェック
- EventBridge (5分おき) → `cmd/reminderworker` : リマインダー送信
- RDS (MySQL) : タスク/リマインダー/会話履歴/OAuthトークン/返信下書き/App Homeの設定値の永続化
- Secrets Manager : Slackトークン等の設定値とDB認証情報

インフラは [infra/](infra/) の AWS CDK で定義し、`main`へのpushで GitHub Actions がデプロイします。
手順・費用・トラブルシューティングは [docs/deploy.md](docs/deploy.md) を参照してください。

## ビルド

```sh
go build ./...
go vet ./...
```
