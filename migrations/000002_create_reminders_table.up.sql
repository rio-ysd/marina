CREATE TABLE reminders (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    slack_channel VARCHAR(64) NOT NULL,
    slack_user VARCHAR(64) NOT NULL,
    message VARCHAR(1000) NOT NULL,
    remind_at DATETIME NOT NULL,
    sent_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_reminders_pending (sent_at, remind_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
