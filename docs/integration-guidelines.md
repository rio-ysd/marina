# 外部サービス連携の実装方針(作法)

marinaに外部サービス連携を追加するときの決まりごと。新しい連携を作る前にこれを読み、
既存実装([internal/tools/mfinvoice.go](../internal/tools/mfinvoice.go) /
[internal/tools/gdrive.go](../internal/tools/gdrive.go))をなぞること。

## 原則: Function Calling(Tool Use)で実装する

外部サービスとの連携は、**サービスのAPIを叩くGoのクライアントを自前で書き、それをFunction Callingの
ツールとしてClaudeに公開する**。この2層構成を崩さない。

```
Claude(Bedrock) ──Function Calling(tool_use)──> marina ──HTTP/SDK──> 外部サービスAPI
```

ツール実行ループはAnthropic SDKのTool Runner(`Beta.Messages.NewToolRunner`、
[internal/agent/agent.go](../internal/agent/agent.go))に任せる。自分でtool_useブロックを
パースするループは書かない。

### なぜMCPを使わないのか

marinaはClaudeをAmazon Bedrock経由で呼び出しており、**AnthropicのMCPコネクタはBedrockでは使えない**。
公式リモートMCPサーバーがあるサービス(例: MoneyForwardクラウド会計)でも、marinaに載せる場合は
Function Callingでの自前実装になる。この前提は
[docs/mf-accounting-api.md](mf-accounting-api.md)にも記録してある。

## レイヤ構成

1つの連携につき3ファイル。`internal/tools/`にサービス名で置く。

| ファイル | 役割 |
|---|---|
| `<service>.go` | ドメイン型、`<Service>Client`インターフェース、`Mock<Service>Client`、`New<Service>Tools()`(ツール定義) |
| `<service>_real.go` | 実APIを呼ぶ`Real<Service>Client`。認証・HTTP・レスポンス変換だけを持つ |
| `<service>_test.go` | `httptest`で実クライアントを検証 |

**ツール定義(`<service>.go`)にHTTPを書かない**。逆に`_real.go`にJSON Schemaを書かない。
この境界があるおかげで、認証情報が無い環境でもモックでツール定義ごと動作確認できる。

参考実装:

- Google Drive — [gdrive.go](../internal/tools/gdrive.go) / [gdrive_real.go](../internal/tools/gdrive_real.go)(サービスアカウント + ドメイン委任)
- Googleカレンダー — [gcalendar.go](../internal/tools/gcalendar.go) / [gcalendar_real.go](../internal/tools/gcalendar_real.go)(日時の正規化あり)
- Googleスプレッドシート — [gsheets.go](../internal/tools/gsheets.go) / [gsheets_real.go](../internal/tools/gsheets_real.go)(追記のみ公開)
- People API — [gpeople.go](../internal/tools/gpeople.go) / [gpeople_real.go](../internal/tools/gpeople_real.go)(参照専用)
- Admin SDK(アカウント作成) — [gdirectory.go](../internal/tools/gdirectory.go) / [userprovision](../internal/userprovision/)(承認フロー付き)
- MoneyForward請求書 — [mfinvoice.go](../internal/tools/mfinvoice.go) / [mfinvoice_real.go](../internal/tools/mfinvoice_real.go)(OAuth2 authorization code + refresh)
- Gmail — [gmail.go](../internal/tools/gmail.go) / [gmail_real.go](../internal/tools/gmail_real.go)

## 実装の作法

### 1. 認証情報が未設定でも起動する

`config.Config`に`HasXxxCredentials()`を足し、[internal/app/app.go](../internal/app/app.go)で
実クライアントとモックを切り替える。認証情報が無い環境で`app.New()`が失敗してはいけない。
同じ認証情報を共有する連携は判定も共有する(Google 4連携はまとめて`HasGoogleCredentials()`)。

```go
var driveClient tools.DriveClient = tools.NewMockDriveClient()
if cfg.HasDriveCredentials() {
    realDriveClient, err := tools.NewRealDriveClient(ctx, ...)
    if err != nil {
        return nil, fmt.Errorf("build drive client: %w", err)
    }
    driveClient = realDriveClient
}
driveTools, err := tools.NewDriveTools(driveClient)
```

モック実装は「モック応答であること」をログに残し、返す値にも`[サンプル]`や`モック`を含める。
本番データと見分けがつかないモックは事故になる。

### 2. ツール名は `<サービス>_<動詞>_<対象>`

外部サービス連携はサービス名のプレフィックスを付ける(`drive_search_files`、`mf_list_invoices`、
`gmail_create_draft`)。プレフィックスが無いのはmarina内部のリソースだけ(`create_task`、`list_tasks`)。

### 3. スキーマでClaudeの間違いを塞ぐ

descriptionは日本語で書き、ツールの説明には**どんな依頼のときに使うか**を具体例で書く。加えて:

- 値が決まっているものは必ず`enum`で縛る。Claudeに生のMIMEタイプやAPI固有のキーを書かせると誤字で0件になる
- APIの語彙をそのままClaudeに見せない。`drive_search_files`の`file_type`は
  `document`/`spreadsheet`のような分かりやすい名前で受け、Go側でMIMEタイプへ変換する
  ([driveMimeTypeFor](../internal/tools/gdrive.go))
- **前提となる別ツールがあるならdescriptionに書く**。「department_idはmf_search_partnersで事前に取得しておくこと」
  のように書くと、Claudeが順番に呼んでくれる
- 不正な値が来ても400/422を返させない。未対応の値はGo側で既定値に落とす
  ([mfinvoice_real.goのrange_key](../internal/tools/mfinvoice_real.go))

### 4. ツールの戻り値でコンテキストを食い潰さない

ツールの戻り値はそのまま次のリクエストの入力になる。無制限に返さない。

- 一覧系は`limit`を受け、既定値と上限を持つ(`normalizeDriveLimit` / `normalizeLimit`。既定20・最大100)
- 本文取得系は文字数上限を持ち、打ち切ったことを結果に明記する(`drive_read_file`の`max_chars`)
- 一覧はJSONではなく整形テキストで返してよい。ただし**後続ツールの入力になるIDは必ず残す**
- 総件数と表示件数が違う場合は明示する。Claudeは一覧の行数を件数として答えてしまう
  ([formatDocumentList](../internal/tools/mfinvoice.go))
- 文字数はバイトではなくrune単位で切る。マルチバイトが途中で割れる

反復上限は`maxToolIterations = 8`([internal/agent/agent.go](../internal/agent/agent.go))。
1つの依頼に3〜4回のツール往復が必要な設計にすると上限に当たるので、往復数が減るようにツールを切る。

### 5. 取り返しがつかない操作はツールにしない

削除、共有権限の変更、送信の確定などは公開しない。marinaはSlackからの曖昧な自然言語で動くので、
誤爆したときに戻せない操作をツールセットに入れない。

- Gmailは下書き作成(`gmail_create_draft`)まで。送信はしない
- Google Driveは検索・読み取り・作成まで。削除と共有設定の変更は実装しない
- スプレッドシートは末尾への追記まで。既存セルの上書き・クリアは実装しない
- カレンダーは自分の予定の作成まで。削除・変更・参加者の招待は実装しない
- 本人として発言する代理返信はツールではなく、DMでのYes/No確認を挟む専用フロー
  ([docs/proxy-reply.md](proxy-reply.md))

### 5-1. どうしても必要な破壊的操作は、確認フローに載せる

業務上どうしても必要な操作(アカウント作成など)は、素のツールにせず**実行の判断を人間に残す**。
Claudeには「依頼するツール」だけを渡し、実行は承認者がSlackのYes/Noボタンを押した後に行う。

- 依頼→pending保存→承認者へ確認DM→ボタン押下で実行、という形にする
  ([internal/userprovision](../internal/userprovision/)、[docs/google-user-provisioning.md](google-user-provisioning.md))
- **承認者以外のボタン押下は無視する**。`ActorUserID`と承認者IDの一致を必ず確認する
- 連打やイベント再送で二重実行しないよう、`pending → 決着` の遷移を取れた場合のみ実行する
- 実行に失敗したら`pending`へ戻し、承認者に理由を伝える
- 承認者が未設定なら**ツール自体を登録しない**。渡さなければClaudeは試みることすらできない
- Claudeが「やった」と誤って報告しないよう、ツールの説明と戻り値の両方に
  「まだ実行されていない」ことを書く

読めない・扱えない対象はエラーにして、Claudeが代替案を案内できるようにする。
エラー文にはユーザーへそのまま伝えられる情報(ファイル名・URLなど)を入れる。

### 6. 相対的な日付はGo側でJST基準に解決する

LambdaのTZはUTCなので、「今月」をClaudeやGoの既定TZに任せると月末月初でずれる。
`time.FixedZone("JST", 9*60*60)`基準でGo側が解決し、既定値(from/to未指定なら今月)を埋める。
システムプロンプトには現在日付を渡してある(`systemPromptWithDate`)。

### 7. システムプロンプトに運用ガイドを1〜2行足す

ツールのdescriptionだけでは足りない「どちらを聞かれているかの区別」や出力の作法は
[internal/agent/agent.go](../internal/agent/agent.go)の`systemPrompt`に足す。
ただしプロンプトは全リクエストに乗るコストなので、ツール固有の説明はdescription側に寄せ、
プロンプトには**ツール選択の判断基準**だけを書く。

### 8. 実クライアントはhttptestで検証する

実APIを叩くテストは書かない。`httptest.NewServer`でAPIを差し替え、
**リクエストの組み立て**と**レスポンスのパース**を検証する。

- HTTP直叩きの場合はクライアント構造体に`baseURL`と`httpClient`を持たせ、テストから差し替える
  ([mfinvoice_test.goのnewTestInvoiceClient](../internal/tools/mfinvoice_test.go))
- Google API Goクライアントの場合は`option.WithHTTPClient` + `option.WithEndpoint(srv.URL+"/")`
  ([gdrive_test.goのnewTestDriveClient](../internal/tools/gdrive_test.go))。
  `WithHTTPClient`を渡すと認証情報の解決が走らないので鍵は不要

最低限テストすること: クエリ/ボディの組み立て、既定値の適用、エスケープ(検索文字列に`'`が入る等)、
数値が文字列で返るような実APIの癖、上限での打ち切り。

## 新しい連携を追加する手順

1. `internal/tools/<service>.go` — 型 + インターフェース + モック + `New<Service>Tools()`
2. `internal/tools/<service>_real.go` — 実クライアント
3. `internal/tools/<service>_test.go` — httptestでの検証
4. `internal/config/config.go` — 環境変数と`HasXxxCredentials()`
5. `internal/app/app.go` — クライアント切り替えとツールの`allTools`への追加
6. `internal/agent/agent.go` — 必要なら`systemPrompt`にツール選択の判断基準を追記
7. OAuth2の場合は`internal/<service>oauth/handler.go`とコールバックの`Mux()`登録、
   トークンは`oauth_tokens`テーブル(provider/accountで分かれているので流用可)
8. `.env.example` / `docker-compose.yml` / `README.md` — 環境変数と有効化手順
9. `docs/<service>.md` — セットアップ手順、スコープ、既知の制約
10. `go build ./... && go vet ./... && gofmt -l . && go test ./...`

## 例外: MCPを使ったほうが早いケース

「Slackのmarinaから使いたい」のではなく「自分がClaude.aiから直接データを触りたい」だけなら、
公式リモートMCPサーバーをClaude.aiのカスタムコネクタとして繋ぐほうが実装ゼロで早い。
marinaに載せる必要があるかを先に判断する。判断材料は[docs/mf-accounting-api.md](mf-accounting-api.md)を参照。
