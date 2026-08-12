# 代理返信フロー(本人宛メンションへのYes/No確認付き自動返信)

対象ユーザー(例: `U05RNEMF1CZ`)宛にメッセージが届いたときに、marinaが返信案を作成して本人にDMで確認し、
Yesが押されたら**本人のアカウントとして**返信を投稿します。対象は次の2種類です。

- **チャンネル**: 本人が参加しているチャンネルでの本人宛メンション(`<@U05RNEMF1CZ> ...`)
- **DM**: 本人への1対1のDM(宛先が本人自身なのでメンションは不要)

## 全体の流れ

1. チャンネルに `<@U05RNEMF1CZ> 見積書の件どうなってますか?` が投稿される、または本人にDMが届く
2. marinaが`message.channels` / `message.im`イベントで受信し、代理返信の対象か判定する
   ([internal/slack/handler.go](../internal/slack/handler.go) の `shouldProxy`)
3. スレッドの文脈を取得し、Claudeが「本人として返す返信案」を生成する
   ([internal/agent/draft.go](../internal/agent/draft.go))
4. 下書きを`reply_drafts`テーブルにpendingで保存し、対象ユーザーへ確認DMを送る
   (Yes/Noボタン + 元メッセージへのリンク付き、[internal/proxyreply/proxyreply.go](../internal/proxyreply/proxyreply.go))
5. 本人が **Yes** を押すとInteractivityのペイロードが届き、User OAuthトークン(`xoxp-`)で
   元のチャンネル(またはDM)のスレッドへ投稿する(=本人の発言として表示される)
6. 確認DMはボタンなしの結果表示(送信済み/取りやめ)に差し替えられる。**No** の場合は投稿せずrejectedで終了する

## 必要な設定

`.env` (Lambdaの場合は環境変数):

| 変数 | 説明 |
|---|---|
| `SLACK_USER_OAUTH_TOKEN` | 対象ユーザー本人が認可したUser OAuthトークン(`xoxp-`)。`chat:write`スコープが必須 |
| `PROXY_REPLY_TARGET_USER_ID` | 対象ユーザーのSlackユーザーID(例: `U05RNEMF1CZ`) |
| `PROXY_REPLY_CHANNEL_IDS` | 対象チャンネルIDのカンマ区切り許可リスト。空ならBotが参加している全チャンネル(DMには影響しない) |
| `PROXY_REPLY_INCLUDE_DM` | 本人へのDMも対象にするか(既定: `true`)。`false`でDMを一切扱わない |

`SLACK_USER_OAUTH_TOKEN`と`PROXY_REPLY_TARGET_USER_ID`の両方が設定されている場合のみ有効化されます。
どちらかが空の場合、対象ユーザー宛のメンションは従来どおり(marina自身の応答対象)として扱われます。

## Slack App側の設定

- **Interactivity & Shortcuts** を有効化する
  - HTTP受信(`cmd/server` / `cmd/lambda`)の場合はRequest URLに `https://<公開ドメイン>/slack/interactions` を設定する
  - **Socket Modeの場合はRequest URLは不要**(入力欄自体が表示されない)。ただしInteractivityのトグルはONにする必要がある
- Event Subscriptions: `message.channels`(チャンネル用)、`message.im`(DM用)
- Bot Token Scopes: 既存のスコープに加えて以下が必要
  - `im:write` — 確認DMを送るため(`conversations.open`)
  - `users:read` — 表示名の解決(`users.info`)
  - `channels:history` / `groups:history` — チャンネルのスレッド文脈取得(`conversations.replies`)
- User Token Scopes:
  - `chat:write` — 本人として投稿するため
  - `im:history` — **DMを対象にする場合に必須**。本人のDMはBotから見えないため、本人のトークンで受信・履歴取得します
- marina(Bot)が対象チャンネルに参加している必要があります(参加していないチャンネルのメッセージは受信できません)

### DMを対象にする場合の注意

`im:history`をUser Token Scopesに追加すると、**marinaは本人の全DMのメッセージイベントを受信します**
(特定の相手だけを購読することはSlack側では指定できません)。プライバシー上の影響を確認してから有効化してください。

受信したDMイベントの扱いは次のとおりです。

| DMの種類 | 挙動 |
|---|---|
| 第三者 → 本人 | **代理返信の対象**(返信案を作成し確認DMを送る) |
| 本人 → 第三者 | 何もしない(marinaは割り込まない) |
| 本人 ↔ marina | 従来どおりmarina自身が秘書として応答する |
| 第三者 → marina | marina自身が応答する(Bot Token Scopesに`im:history`がある場合のみ届く) |

「本人 → 第三者」のDMにmarinaが通常応答で割り込まないよう、イベントの`authorizations`
(どのトークンに配信されたか)とmarinaとのDMチャンネルIDを見て判定しています
([internal/slack/handler.go](../internal/slack/handler.go) の `isTargetPrivateDM`)。

グループDM(mpim)は対象外です。

## 受信方式(HTTP / Socket Mode)

イベントとボタン押下の受信方式は2通りあり、どちらも同じ処理
(`slack.Handler.HandleEvent` / `slack.InteractionHandler.HandleInteraction`)に合流します。

| 方式 | エントリポイント | Request URL | 備考 |
|---|---|---|---|
| HTTP | `cmd/server`, `cmd/lambda` | 必要(`/slack/events`, `/slack/interactions`) | 本番構成。ローカルではngrok等でトンネルが必要 |
| Socket Mode | `cmd/socketmode` | **不要** | `SLACK_APP_TOKEN`(`xapp-`)が必要。常時接続のためLambdaでは使えない |

Socket Modeでの起動:

```sh
docker compose --env-file .env --profile socketmode up --build socketmode
```

MySQLだけComposeで起動し、ホストから直接動かすこともできます。

```sh
docker compose --env-file .env up -d mysql migrate
set -a; source .env; set +a
DB_DSN="marina:marina@tcp(127.0.0.1:3306)/marina?parseTime=true&loc=Local" go run ./cmd/socketmode
```

Socket Modeでは署名検証(`SLACK_SIGNING_SECRET`)は使われませんが、`config.Load`の必須項目なので値は設定しておいてください。

## 動作上の注意

- **グループDM(`mpim`)は対象外**です
- メッセージ内でmarina自身もメンションされている場合(`<@marina> <@U05RNEMF1CZ> ...`)は、
  `app_mention`側でmarinaが通常応答するため、代理返信フローは起動しません
- 対象ユーザー本人の発言に含まれるメンションは無視します(自分宛のメモ等で起動しないため)
- 返信は元メッセージのスレッドに投稿されます(スレッド外のメンションの場合は、そのメッセージのスレッドになります)
- Yes/Noの判定は`reply_drafts.status`の`pending`→`approved`/`rejected`の条件付きUPDATEで排他制御しており、
  ボタン連打やSlackのイベント再送でも二重送信されません
- 確認DMには元メッセージへのパーマリンク(`chat.getPermalink`)を付けています。**No**で取りやめた場合も
  自分で対応できるようリンクを残します
- 投稿に失敗した場合はstatusをpendingへ戻し、確認DMにエラーを表示します(もう一度Yesを押せます)
- ボタンを押せるのは対象ユーザー本人のみです(`callback.User.ID`と`PROXY_REPLY_TARGET_USER_ID`を照合)

## テーブル

`reply_drafts` ([migrations/000005_create_reply_drafts_table.up.sql](../migrations/000005_create_reply_drafts_table.up.sql))
に下書き・ステータス・確認DMの位置・送信済みメッセージのtsを保存します。Lambdaは実行ごとにプロセスが変わるため、
承認待ちの状態はメモリではなくDBに置いています。
