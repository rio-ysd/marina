# Googleカレンダー連携

予定の確認と自分の予定の作成をFunction Callingツールとして提供する。
実装方針の背景は[docs/integration-guidelines.md](integration-guidelines.md)を参照。

## 認証方式

Gmail / Drive / スプレッドシートと同じGoogle Workspaceサービスアカウント + ドメイン委任を使う。
環境変数は共通(`GOOGLE_SERVICE_ACCOUNT_JSON` / `GOOGLE_IMPERSONATED_USER`)で、
カレンダー用に増えるものはない。セットアップ手順は[docs/google-drive.md](google-drive.md)と同じ。

必要なスコープ:

```
https://www.googleapis.com/auth/calendar
```

予定を作成するため、参照専用の `calendar.readonly` では足りない。削除・変更のツールは公開していない。

対象カレンダーは代理対象ユーザー(`GOOGLE_IMPERSONATED_USER`)のメインカレンダー(`primary`)固定。

## 提供するツール

| ツール | 用途 | 対応するAPI |
|---|---|---|
| `calendar_list_events` | 期間内の予定を開始時刻順に取得 | `GET /calendars/primary/events` |
| `calendar_create_event` | 自分の予定を作成 | `POST /calendars/primary/events` |

### 日時の扱い

LambdaのTZはUTCなので、日時はすべてGo側でJST基準に解決する。

- `calendar_list_events` の `from`/`to` は `YYYY-MM-DD`。**省略すると今日(JST)** が対象
- `to` はその日を含める。APIの `timeMax` には翌日0時を渡す(23:59:59だと終日予定を取りこぼす)
- `calendar_create_event` の `start`/`end` は `YYYY-MM-DDTHH:MM`(JST)。終日予定は `YYYY-MM-DD`
- 終日と時刻付きを混ぜた指定はGo側で弾く
- 終日予定の `end` はAPI仕様上排他的なので、指定日を含めるようGo側で+1日する
  (「8/14に有給」なら `end.date` は `2026-08-15` を送る)
- `SingleEvents(true)` で繰り返し予定を個々の回に展開する。展開しないと「今日の予定」に出てこない

## 実装しなかったもの

- **予定の削除・変更** — Slackの曖昧な指示で誤爆すると戻せない。必要になったら
  [代理返信フロー](proxy-reply.md)と同じYes/No確認を挟む形にする
- **参加者の招待** — 他人のカレンダーに予定が入る外向きの操作になる。作成時は `sendUpdates=none`
  を明示し、招待メールが飛ばないようにしている
- **空き時間の検索(Freebusy)** — `calendar_list_events` の結果からClaudeが判断できるため未実装
- **メイン以外のカレンダー** — `primary` 固定

## ファイル

- [internal/tools/gcalendar.go](../internal/tools/gcalendar.go) — 型、`CalendarClient`、モック、ツール定義、日時パース
- [internal/tools/gcalendar_real.go](../internal/tools/gcalendar_real.go) — `RealCalendarClient`(Calendar API v3)
- [internal/tools/gcalendar_test.go](../internal/tools/gcalendar_test.go) — httptestでの検証
