-- Google Workspaceアカウント作成の承認待ちリクエスト。
-- Slackの曖昧な指示でアカウントが作られないよう、承認者のYes/No確認を挟むための状態を保持する。
-- 一時パスワードは保存しない(作成時に生成し、承認者へのDMで一度だけ通知する)。
CREATE TABLE user_creation_requests (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    requester_user VARCHAR(64) NOT NULL,
    request_channel VARCHAR(64) NOT NULL,
    primary_email VARCHAR(255) NOT NULL,
    given_name VARCHAR(128) NOT NULL,
    family_name VARCHAR(128) NOT NULL,
    org_unit_path VARCHAR(255) NULL,
    status ENUM('pending', 'approved', 'rejected') NOT NULL DEFAULT 'pending',
    approval_channel VARCHAR(64) NULL,
    approval_ts VARCHAR(32) NULL,
    created_email VARCHAR(255) NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at DATETIME NULL,
    INDEX idx_user_creation_requests_pending (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
