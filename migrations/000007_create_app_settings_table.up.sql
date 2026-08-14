-- marinaの振る舞いをコード変更なしで調整するための設定値。
-- Slack App Homeのタブから編集し、朝のダイジェストとSlack会話のシステムプロンプトに差し込む。
-- 現在のキーは custom_instructions(ユーザーからの追加指示)のみ。
CREATE TABLE app_settings (
    name VARCHAR(64) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_by VARCHAR(64) NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
