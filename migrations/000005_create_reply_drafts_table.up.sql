CREATE TABLE reply_drafts (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    target_user VARCHAR(64) NOT NULL,
    source_channel VARCHAR(64) NOT NULL,
    source_thread_ts VARCHAR(32) NOT NULL,
    source_message_ts VARCHAR(32) NOT NULL,
    requester_user VARCHAR(64) NOT NULL,
    request_text MEDIUMTEXT NOT NULL,
    draft_text MEDIUMTEXT NOT NULL,
    status ENUM('pending', 'approved', 'rejected') NOT NULL DEFAULT 'pending',
    approval_channel VARCHAR(64) NULL,
    approval_ts VARCHAR(32) NULL,
    sent_ts VARCHAR(32) NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at DATETIME NULL,
    INDEX idx_reply_drafts_pending (target_user, status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
