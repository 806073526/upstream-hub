-- Source NewAPI events retained for billing audit and reconciliation.
CREATE TABLE IF NOT EXISTS newapi_billing_events (
    id                    BIGSERIAL PRIMARY KEY,
    event_key             VARCHAR(160) NOT NULL UNIQUE,
    source_log_id         BIGINT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL,
    bucket_start          TIMESTAMPTZ NOT NULL,
    bucket_end            TIMESTAMPTZ NOT NULL,
    event_type            VARCHAR(16) NOT NULL,
    channel_id            INTEGER NOT NULL,
    upstream_channel_id   BIGINT,
    mapping_status        VARCHAR(16) NOT NULL DEFAULT 'unmapped',
    "group"              VARCHAR(256) NOT NULL,
    model_name            VARCHAR(256) NOT NULL,
    effective_group_ratio NUMERIC(20,8) NOT NULL,
    ratio_source          VARCHAR(32) NOT NULL,
    normalization_status  VARCHAR(16) NOT NULL,
    quota                 BIGINT NOT NULL,
    charged_usd           NUMERIC(20,8) NOT NULL,
    normalized_usd        NUMERIC(20,8) NOT NULL,
    credit_usd_per_cny    NUMERIC(20,8) NOT NULL,
    sale_cny              NUMERIC(20,8) NOT NULL,
    user_id               INTEGER NOT NULL DEFAULT 0,
    token_name            VARCHAR(256) NOT NULL DEFAULT '',
    request_id            VARCHAR(128) NOT NULL DEFAULT '',
    upstream_request_id   VARCHAR(128) NOT NULL DEFAULT '',
    collected_at          TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_newapi_billing_events_window
    ON newapi_billing_events (bucket_start, channel_id, event_type);
CREATE INDEX IF NOT EXISTS idx_newapi_billing_events_source
    ON newapi_billing_events (source_log_id, created_at);
