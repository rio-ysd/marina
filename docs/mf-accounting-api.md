# MoneyForwardクラウド会計API連携(保留中の調査メモ)

marinaに会計API連携を実装する場合の調査結果。**2026-08-13時点で未実装・保留**。
請求書API連携([mf-invoice-oauth.md](mf-invoice-oauth.md))とは別プロダクト・別APIで、
扱えるデータが異なる。

## 判明していること

### 認可サーバーは請求書APIと共通

既存の[internal/mfoauth/handler.go](../internal/mfoauth/handler.go)のOAuth設定がそのまま使える。

```
authorize: https://api.biz.moneyforward.com/authorize
token:     https://api.biz.moneyforward.com/token
```

アプリの登録先も同じ[アプリポータル](https://app-portal.moneyforward.com/)で、
1つのアプリ登録で複数プロダクトのスコープを要求できる(と思われる。要検証)。
そのため会計を追加する場合、請求書の認可をやり直す必要がある点に注意
(スコープを増やすと再認可が必要)。

### スコープ

| スコープ | 用途 |
|---|---|
| `mfc/accounting/journal.write` | 仕訳の登録・更新・削除 |
| `mfc/accounting/accounts.read` | 勘定科目・補助科目の参照 |
| `mfc/accounting/departments.read` | 部門の参照 |
| `mfc/accounting/trade_partners.read` | 取引先の参照 |
| `mfc/accounting/taxes.read` | 税区分の参照 |

参考: 請求書APIは `mfc/invoice/data.write`。

### 会計APIでできること

- **参照**: 事業者情報、会計期間、仕訳(一覧・個別)、試算表・推移表、勘定科目・補助科目、
  取引先、部門、税区分、入出金明細一覧
- **更新**: 仕訳の作成・更新、入出金明細の作成、明細からの仕訳生成

**見積書・請求書の作成はできない**(請求書プロダクトの機能)。

## 不明なこと(実装前に必要)

公式リファレンス([developers.api-accounting.moneyforward.com](https://developers.api-accounting.moneyforward.com/))は
ログインが必要で、外部から取得できなかった。実装するには以下が必要。

1. APIのベースURL(請求書APIは `https://invoice.moneyforward.com/api/v3`)
2. 仕訳登録のメソッド・パス・リクエストボディの形
3. 勘定科目 / 部門 / 取引先 の一覧取得のパス
4. 試算表などレポートのパス
5. 事業者(office)の指定方法(ヘッダーかパスパラメータか)

OpenAPI/Swagger仕様が取得できるならそれが最も確実。

## 実装方針(決まったら)

請求書連携と同じ構造をなぞる。

- `internal/tools/mfaccounting.go` — インターフェース + モック + ツール定義
- `internal/tools/mfaccounting_real.go` — 実HTTPクライアント(oauth2.TokenSourceでの自動リフレッシュは
  `mfinvoice_real.go`と同じパターン)
- `internal/mfoauth/handler.go` — `Scopes`に会計スコープを追加
- `internal/app/app.go` — ツールの配線
- `oauth_tokens`テーブルはprovider/accountで分かれているため、そのまま流用できる

## 代替案: 公式MCPサーバー

MoneyForwardは会計API用のリモートMCPサーバーを提供している(2026年3月に全プランで正式提供開始)。

```
alpha: https://alpha.mcp.developers.biz.moneyforward.com/mcp/ca/v3
```

Claude.aiのカスタムコネクタとして接続すれば**実装ゼロ**で会計データを扱える。
利用にはアプリポータルの「全権管理」権限でのユーザー認可が必要。追加料金なし。

ただし**marinaからは使えない**。marinaはBedrock経由でClaudeを呼んでおり、
AnthropicのMCPコネクタはBedrockでは非対応のため。marinaに載せるなら自前実装が必要。

「Slackから使いたいのではなく、自分がClaude.aiから会計データを触れれば良い」場合は、
MCPコネクタを繋ぐ方が早い。

## 参考リンク

- [マネーフォワード クラウド会計API](https://developers.api-accounting.moneyforward.com/)
- [マネーフォワード クラウド会計APIについて(サポート)](https://biz.moneyforward.com/support/account/guide/others/ot09.html)
- [マネーフォワード クラウド会計MCPサーバーについて(サポート)](https://biz.moneyforward.com/support/account/guide/others/ot10.html)
- [アプリポータル](https://app-portal.moneyforward.com/) / [連携用アプリを登録する](https://biz.moneyforward.com/support/app-portal/guide/g011.html)
- [開発者サイト](https://developers.biz.moneyforward.com/)
