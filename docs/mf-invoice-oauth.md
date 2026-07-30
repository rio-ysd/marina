# MoneyForwardクラウド請求書API連携(OAuth2)実装状況

## 背景

`internal/tools/mfinvoice.go`にはMFInvoiceClientのインターフェースとモック実装のみが存在し、実クライアントは未実装だった。

当初`.env`の`MF_API_KEY`によるAPIキー方式を検討したが、検証の結果以下が判明したため不採用とした。

- `MF_API_KEY`をJWTに交換すると`services`スコープに`invoice`が含まれず、請求書APIへの直接アクセスが`401 token_rejected`で拒否される
- 請求書APIのOpenAPI仕様(`securitySchemes`)は`AccessToken`というOAuth2 authorizationCodeフローのみを定義しており、APIキー方式は仕様上サポートされていない

「OAuthはSlackから使えないのでは」という懸念に対しては、「初回のみブラウザで認可し、以降はrefresh_tokenで自動更新される」設計で解消し、OAuth2フローでの実装に合意した。

## 実装済みの内容

### 新規ファイル

- `internal/storage/oauth_token_repo.go` — OAuth2トークン(access_token/refresh_token/expires_at)のDB永続化。既存の`migrations/000004_create_oauth_tokens_table`をそのまま利用
- `internal/mfoauth/handler.go` — 認可コールバック用HTTPハンドラ
  - `/mf/oauth/start` — stateを生成しMoneyForwardの認可画面へリダイレクト
  - `/mf/oauth/callback` — state検証→authorization code交換→トークンをDBに保存
- `internal/tools/mfinvoice_real.go` — `RealMFInvoiceClient`
  - DBからトークンを取得し、`golang.org/x/oauth2`の`TokenSource`で期限切れ時に自動リフレッシュ、更新分はDBへ書き戻す
  - `GET /partners`・`POST /quotes`・`POST /invoice_template_billings`をHTTPで呼び出す(ベースURL: `https://invoice.moneyforward.com/api/v3`)

### 変更ファイル

- `internal/tools/mfinvoice.go` — 実APIスキーマ(`department_id`、品目の`price`/`quantity`/`excise`等)に合わせて構造体・インターフェースを再設計。取引先検索用の`mf_search_partners`ツールを新設(取引先名→department_id解決はLLM側の2ステップ呼び出しに委ねる方針)
- `internal/config/config.go` — `MF_OAUTH_REDIRECT_URI`を追加、`HasMFCredentials()`の判定に組み込み
- `internal/app/app.go` — OAuthトークンリポジトリ/RealMFInvoiceClient/mfoauth.Handlerの配線、`App.Mux()`メソッドを新設(server/lambda共通のルーティング)
- `cmd/server/main.go` — `a.Mux()`を使うように簡素化
- `cmd/lambda/main.go` — API GatewayイベントからクエリパラメータをそのままLambdaが握り潰していた不具合を修正(`code`/`state`が消えていた)。`Mux()`経由でパスルーティングされるように変更
- `internal/agent/agent.go` — systemPromptに「見積書・請求書作成前に`mf_search_partners`でdepartment_idを確認する」運用ガイドを追記
- `.env` / `.env.example` / `docker-compose.yml` / `README.md` — `MF_OAUTH_REDIRECT_URI`の追記とOAuth運用方針の説明

### 検証済み

- `go build ./...` / `go vet ./...` / `gofmt -l .` — いずれもエラーなし

## 次回以降に対応すること(ユーザー作業)

1. MoneyForwardアプリポータルでOAuthクライアントを登録し、リダイレクトURI(`https://<公開ドメイン>/mf/oauth/callback`)を設定してClient ID/Secretを取得
2. `.env`に`MF_CLIENT_ID` / `MF_CLIENT_SECRET` / `MF_OAUTH_REDIRECT_URI`を設定
3. `docker compose --env-file .env up --build`で起動、`ngrok http 8091`等でトンネル
4. ブラウザで`https://<公開ドメイン>/mf/oauth/start`を開き、MoneyForwardの認可画面で許可 → `/mf/oauth/callback`が完了メッセージを表示することを確認
5. `oauth_tokens`テーブルにaccess_token/refresh_tokenが保存されていることを確認
6. Slackで取引先検索(`mf_search_partners`)・見積書/請求書作成(`mf_create_estimate`/`mf_create_invoice`)の動作確認
7. `expires_at`を過去日時に書き換えてリフレッシュ動作(自動更新→DB更新→API成功)を確認
