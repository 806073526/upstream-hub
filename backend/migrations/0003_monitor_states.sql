-- 渠道级监控冷却状态。余额、倍率、用量和登录任务共享一行。
CREATE TABLE IF NOT EXISTS channel_monitor_states (
    channel_id         BIGINT PRIMARY KEY,
    failure_count      INTEGER NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ,
    last_failure_type  VARCHAR(32),
    last_error         TEXT,
    last_error_key     VARCHAR(128),
    last_checked_at    TIMESTAMPTZ,
    last_success_at    TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
