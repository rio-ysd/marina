# People API連携(社内メンバー・連絡先の検索)

「〇〇さんのメアド教えて」「〇〇さんって何部だっけ」に答えるための参照専用の連携。
廃止されたGoogle+ API / Google+ Domains APIの後継にあたる(経緯は[README](../README.md)参照)。
実装方針の背景は[docs/integration-guidelines.md](integration-guidelines.md)を参照。

## 認証方式

他のGoogle連携と同じサービスアカウント + ドメイン委任。環境変数は共通で増えるものはない。
セットアップ手順は[docs/google-drive.md](google-drive.md)と同じ。

必要なスコープ(いずれも参照専用):

```
https://www.googleapis.com/auth/directory.readonly
https://www.googleapis.com/auth/contacts.readonly
```

**Google CloudコンソールでPeople APIの有効化も必要**。スコープを登録しても、APIが有効化されて
いないと `403 SERVICE_DISABLED`(People API has not been used in project ... before or it is disabled)
になる。スコープ未登録の `unauthorized_client` とは別の失敗なので、エラー文で切り分けること。

## 提供するツール

| ツール | 用途 | 対応するAPI |
|---|---|---|
| `people_search_directory` | 社内メンバー(Workspaceユーザー)を名前/メールで検索 | `people:searchDirectoryPeople` |
| `people_search_contacts` | 代理対象ユーザー個人の連絡先を検索 | `people:searchContacts` |

- 検索は名前・メールの前方一致。姓だけ・名だけでも引ける
- 既定10件・最大30件(APIのpageSize上限が30)
- `readMask` は `names,emailAddresses,organizations,phoneNumbers`。**指定しないとフィールドが何も返らない**
- ディレクトリ検索の `sources` にはドメインプロフィールとドメイン共有連絡先の両方を指定する
- 会社名・部署・役職は入っているものだけを ` / ` で連結する
- 0件のときは「見つからなかった」と明示する(Claudeが推測でメールアドレスを作らないため)

## 実装しなかったもの

- **連絡先の作成・更新・削除** — 参照専用に絞っている
- **プロフィール写真の取得** — Slackでの回答に不要
- **Admin SDK Directoryでのユーザー一覧取得** — 検索用途はPeople APIで足りる。
  Admin SDKはアカウント作成のみに使う([docs/google-user-provisioning.md](google-user-provisioning.md))

## ファイル

- [internal/tools/gpeople.go](../internal/tools/gpeople.go) — 型、`PeopleClient`、モック、ツール定義
- [internal/tools/gpeople_real.go](../internal/tools/gpeople_real.go) — `RealPeopleClient`(People API v1)
- [internal/tools/gpeople_test.go](../internal/tools/gpeople_test.go) — httptestでの検証
