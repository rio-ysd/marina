# App Homeから追加の運用ルールを編集する

marinaのSlack App Homeタブに「追加の運用ルール」を書いておくと、その内容が
**Slackでの応答**と**朝のメールダイジェスト**の両方のシステムプロンプトに差し込まれます。
「SALON BOARD関連のメールは重要ではないので、既読にするだけでよい」のような運用ルールを、
コード変更・再デプロイなしで足したり消したりするための仕組みです。

## 使い方

1. Slackの左サイドバーから **marina** を開き、**ホーム** タブを選ぶ
2. 「編集する」ボタンを押す(編集できるのは `HOME_ADMIN_SLACK_USER_ID` に指定したユーザーのみ)
3. モーダルに1行1ルールで書いて「保存」を押す

保存すると即座に反映されます(次のSlackでの応答、および翌朝のダイジェストから有効)。
空欄で保存すると「指示なし」に戻ります。

### 書き方

指示はシステムプロンプトの末尾に、既定のルールより**後ろ**に置かれます。
既定の判断(「不要なメールか」「返信が必要か」)を上書きさせたい内容を書いてください。

```
SALON BOARD関連のメールは重要ではないので、既読にするだけでよい
経理からのメールは必ず要返信に入れる
社外の初回問い合わせは断定せず、折り返す旨だけ返す
```

上限は3000文字です。システムプロンプトは全リクエストに乗るため、
長く書くほど毎回のトークン消費が増えます。効くルールだけを残してください。

## 設定

| 環境変数 | 用途 |
|---|---|
| `HOME_ADMIN_SLACK_USER_ID` | 追加指示を編集できるSlackユーザーID(例: `U05RNEMF1CZ`) |

未設定の場合、Homeタブは閲覧のみになります(誰にも編集ボタンが出ません)。
指定したユーザー以外にはボタンを出さないうえ、ボタン押下とモーダル送信の両方で
`ActorUserID` と照合するため、古い画面から送信されても保存されません。

## Slack App側の設定

- **App Home** — 「Home Tab」を有効化する
- **Event Subscriptions** — `app_home_opened` を購読する(Homeタブを開いたときに描き直すため)
- **Interactivity & Shortcuts** — 有効化し、Request URLに `https://<公開ドメイン>/slack/interactions`
  を設定する(代理返信のYes/Noボタンと同じURLで構いません)

Bot Token Scopeの追加は基本的に不要です(`views.publish` / `views.open` はBotトークンで呼べ、
`app_home_opened` はmarinaが既に持っている `im:history` で配信されます)。
イベント購読を追加するとSlackが再インストールを求めるので、その場合はワークスペースに入れ直してください。

## 保存先

`app_settings` テーブル(`migrations/000007_create_app_settings_table.up.sql`)に
`custom_instructions` というキーで1行だけ保存します。更新者と更新日時も記録し、Homeタブに表示します。

読み書きは [internal/instructions](../internal/instructions/) が担当し、
[internal/agent](../internal/agent/agent.go) と [internal/morningdigest](../internal/morningdigest/morningdigest.go)
がプロンプト組み立て時に読み出します。追加指示はあくまで補助なので、DBの読み取りに失敗した場合は
ログだけ残して**指示なしとして処理を続行**します(朝のダイジェストが丸ごと止まるのを避けるため)。

## 実装上の注意

Homeタブの編集モーダルは `trigger_id` の有効期限が**3秒**しかありません。
本番構成ではSlackのInteractivityを受信Lambda(`cmd/lambda`)からワーカーLambda(`cmd/eventworker`)へ
非同期invokeしていますが、ワーカーの起動を待つと `expired_trigger_id` でモーダルが開きません。
そのためApp Home由来の操作だけは `InteractionHandler.TryHandleSync` で受信Lambda側で直接処理します。
ここに載せてよいのは、Slack APIを1〜2回叩くだけで終わる処理に限ります
(Claudeの呼び出しなど時間のかかる処理を足すと3秒に収まらなくなります)。

モーダルの送信(`view_submission`)に対しては**空のボディで200を返す**必要があります。
`ok` のようなテキストを返すとSlackがパースに失敗し、モーダルに「接続に問題が発生しました」と表示されます。
