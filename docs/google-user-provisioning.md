# Google Workspaceアカウント作成(承認フロー付き)

Slackから依頼したアカウント作成を、**承認者のYes/No確認を経てから**実行する連携。
Admin SDK Directory APIを使う。実装方針の背景は[docs/integration-guidelines.md](integration-guidelines.md)を参照。

## なぜ承認フローを挟むのか

[integration-guidelines.md](integration-guidelines.md)の「取り返しがつかない操作はツールにしない」に
アカウント作成は該当する。一方で作成自体は業務上必要なので、**素のツールとして公開せず、
実行の判断を人間に残す**形にした。代理返信フロー([docs/proxy-reply.md](proxy-reply.md))と同じ構造。

```
Slackで依頼
  → Claudeが directory_request_user_creation を呼ぶ(この時点では作らない)
  → 依頼をDBにpendingで保存
  → 承認者へYes/Noボタン付きのDM
  → 承認者がYes → Admin SDKで作成 → 一時パスワードをDMに表示
     承認者がNo → 何もせず取りやめ
```

Claudeが呼べるのは依頼ツールだけで、作成を直接実行するツールは存在しない。

## 有効化の条件

**`USER_PROVISION_APPROVER_SLACK_USER_ID` が未設定の場合、ツール自体を登録しない。**
Claudeは作成を試みることすらできず、起動ログに `user provisioning disabled` が出る。

| 環境変数 | 内容 |
|---|---|
| `USER_PROVISION_APPROVER_SLACK_USER_ID` | 承認できる唯一のSlackユーザーID(例: `U05RNEMF1CZ`) |
| `GOOGLE_SERVICE_ACCOUNT_JSON` / `GOOGLE_IMPERSONATED_USER` | 他のGoogle連携と共通 |

本番(Lambda)では設定値をSecrets Managerの `marina/app` から読むため、**このキーは
Secrets Managerのシークレットに手で追加する**(CDKの`generateSecretString`はシークレット作成時
にしか使われないので、テンプレートに足しても既存シークレットには反映されない)。
キーが無い場合は機能が無効のままになるだけで、他の機能には影響しない。

承認者以外がボタンを押しても無視される。ここが破られると誰でもアカウントを作れてしまうため、
`ActorUserID` と承認者IDの一致を必ず確認している。

## 必要な権限とスコープ

```
https://www.googleapis.com/auth/admin.directory.user
```

セットアップに3つ必要で、**どれが欠けていても失敗する**。エラー文で切り分けること。

1. ドメイン委任の許可スコープに上記を登録 → 未登録なら `unauthorized_client`
2. Google Cloudコンソールで **Admin SDK API** を有効化 → 未有効化なら `403 SERVICE_DISABLED`
3. 代理対象ユーザー(`GOOGLE_IMPERSONATED_USER`)に**ユーザー管理権限**を付与 → 無ければ `403 insufficient permission`

### 権限は最小に絞る

3について、特権管理者を付けるのではなく、**「ユーザーの作成」だけを持つカスタム管理者ロール**を
作って付与することを勧める。理由は、このスコープに**作成専用の指定が存在しない**ため:
`admin.directory.user` 1つで更新・停止・削除まで技術的に可能になる。
サービスアカウント鍵が漏れた場合の影響範囲を、ロール側で絞っておく。

marinaが公開するツールは作成依頼のみなので、通常運用での操作範囲はツールセットで限定されている。

## 一時パスワードの扱い

- 作成時に暗号論的乱数(`crypto/rand`)で20文字を生成する
- 読み間違えやすい文字(`0/O`、`1/l/I`)は除外している
- `ChangePasswordAtNextLogin=true` を設定し、初回ログインで変更させる
- **DBには保存しない。** 承認者へのDMに一度だけ表示されるので、そこから本人へ安全な経路で渡す

Slackに一時パスワードが残る点はトレードオフ。初回ログインで無効化される前提で許容している。
気になる場合は、DMを削除する運用にするか、パスワードを表示せず管理コンソールから
再発行する運用に変える(その場合は`updateApprovalMessage`の文面を変更する)。

## 二重作成・失敗時の扱い

- ボタン連打やイベント再送で二重に作らないよう、`pending → approved` の遷移を取れた場合のみ実行する
  (`ClaimDecision`。代理返信フローと同じ排他制御)
- 作成に失敗したら `pending` へ戻し、承認者のDMに失敗理由を出す。もう一度承認できる
- 承認者に確認を出す前に既存アカウントの重複を弾く(承認後に失敗すると、承認者は作られたと
  思ったままになる)

## 提供するツール

| ツール | 用途 |
|---|---|
| `directory_request_user_creation` | アカウント作成の**依頼**。承認DMを送るだけで作成はしない |

入力は `primary_email` / `family_name` / `given_name` と、任意の `org_unit_path`。
メールアドレスの形式と姓名の有無をGo側で検証し、`org_unit_path` は先頭にスラッシュを補う
(無いとAPIが受け付けない)。

## 実装しなかったもの

- **更新・停止・削除** — 復旧できない。管理コンソールから行う
- **グループへのメンバー追加** — 別スコープ(`admin.directory.group.member`)。必要になったら追加する
- **一覧・検索** — 検索はPeople API側([docs/google-people.md](google-people.md))で足りる
- **承認者の複数指定** — 現状は1人。増やすなら許可リストにする

## ファイル

- [internal/tools/gdirectory.go](../internal/tools/gdirectory.go) — 型、`DirectoryClient`、`UserCreationApprover`、依頼ツール、入力検証
- [internal/tools/gdirectory_real.go](../internal/tools/gdirectory_real.go) — `RealDirectoryClient`(Admin SDK Directory v1)、一時パスワード生成
- [internal/userprovision/userprovision.go](../internal/userprovision/userprovision.go) — 承認フロー(確認DM・ボタン処理・作成実行)
- [internal/storage/user_creation_request_repo.go](../internal/storage/user_creation_request_repo.go) — 承認待ちリクエストの永続化
- [migrations/000006_create_user_creation_requests_table.up.sql](../migrations/000006_create_user_creation_requests_table.up.sql)
- [internal/slack/interactions.go](../internal/slack/interactions.go) — ボタン押下の振り分け
