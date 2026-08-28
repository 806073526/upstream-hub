-- NewAPI consumption facts and settlement watermark. This ledger is separate
-- from usage_buckets, which stores upstream-reported cost/usage data.
CREATE TABLE IF NOT EXISTS newapi_billing_buckets (
    id                      BIGSERIAL PRIMARY KEY,
    fact_key                VARCHAR(128) NOT NULL UNIQUE,
    bucket_start            TIMESTAMPTZ NOT NULL,
    bucket_end              TIMESTAMPTZ NOT NULL,
    resolution_seconds      INTEGER NOT NULL,
    new_api_channel_id      INTEGER NOT NULL,
    upstream_channel_id     BIGINT,
    mapping_status          VARCHAR(16) NOT NULL DEFAULT 'unmapped',
    "group"                 VARCHAR(256) NOT NULL,
    model_name              VARCHAR(256) NOT NULL,
    effective_group_ratio   NUMERIC(20,8) NOT NULL,
    ratio_source            VARCHAR(32) NOT NULL,
    normalization_status    VARCHAR(16) NOT NULL,
    consume_quota           BIGINT NOT NULL,
    refund_quota            BIGINT NOT NULL,
    net_quota               BIGINT NOT NULL,
    event_count             BIGINT NOT NULL,
    quota_per_unit          NUMERIC(20,8) NOT NULL,
    charged_usd             NUMERIC(20,8) NOT NULL,
    normalized_usd          NUMERIC(20,8) NOT NULL,
    credit_usd_per_cny      NUMERIC(20,8) NOT NULL,
    sale_cny                NUMERIC(20,8) NOT NULL,
    complete                BOOLEAN NOT NULL DEFAULT TRUE,
    collected_at            TIMESTAMPTZ NOT NULL,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_newapi_billing_window
    ON newapi_billing_buckets (bucket_start, resolution_seconds, new_api_channel_id, "group", model_name);
CREATE INDEX IF NOT EXISTS idx_newapi_billing_status
    ON newapi_billing_buckets (normalization_status, bucket_start);

-- Append-only mapping observations let historical billing resolve the upstream
-- account that was active when a request was served. An unmapped observation
-- deliberately stores a NULL upstream_channel_id so an old binding is not
-- reused forever after a channel is detached.
CREATE TABLE IF NOT EXISTS billing_mapping_snapshots (
    id                   BIGSERIAL PRIMARY KEY,
    mapping_key          VARCHAR(128) NOT NULL UNIQUE,
    new_api_channel_id   INTEGER NOT NULL,
    upstream_channel_id  BIGINT,
    mapping_status       VARCHAR(16) NOT NULL,
    upstream_group       VARCHAR(256) NOT NULL DEFAULT '',
    observed_at          TIMESTAMPTZ NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_billing_mapping_lookup
    ON billing_mapping_snapshots (new_api_channel_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_mapping_status
    ON billing_mapping_snapshots (mapping_status, observed_at);

-- Reconciled sales/cost rows and the daily dashboard read model.
CREATE TABLE IF NOT EXISTS profit_buckets (
    id                    BIGSERIAL PRIMARY KEY,
    billing_fact_key      VARCHAR(128) NOT NULL UNIQUE,
    bucket_start          TIMESTAMPTZ NOT NULL,
    bucket_end            TIMESTAMPTZ NOT NULL,
    resolution_seconds    INTEGER NOT NULL,
    new_api_channel_id    INTEGER NOT NULL,
    upstream_channel_id   BIGINT,
    mapping_status        VARCHAR(16) NOT NULL,
    "group"              VARCHAR(256) NOT NULL,
    model_name            VARCHAR(256) NOT NULL,
    normalization_status  VARCHAR(16) NOT NULL,
    sale_cny              NUMERIC(20,8) NOT NULL,
    cost_usd              NUMERIC(20,8) NOT NULL,
    cost_cny              NUMERIC(20,8) NOT NULL,
    profit_cny            NUMERIC(20,8) NOT NULL,
    credit_usd_per_cny    NUMERIC(20,8) NOT NULL,
    allocation_status     VARCHAR(24) NOT NULL,
    complete              BOOLEAN NOT NULL DEFAULT FALSE,
    calculated_at         TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_profit_window
    ON profit_buckets (bucket_start, resolution_seconds, new_api_channel_id);
CREATE INDEX IF NOT EXISTS idx_profit_status
    ON profit_buckets (allocation_status, bucket_start);

CREATE TABLE IF NOT EXISTS profit_daily_snapshots (
    id                    BIGSERIAL PRIMARY KEY,
    day_start             TIMESTAMPTZ NOT NULL UNIQUE,
    sale_cny              NUMERIC(20,8) NOT NULL,
    cost_cny              NUMERIC(20,8) NOT NULL,
    profit_cny            NUMERIC(20,8) NOT NULL,
    settled_sale_cny      NUMERIC(20,8) NOT NULL,
    unmapped_sale_cny     NUMERIC(20,8) NOT NULL,
    unsettled_sale_cny    NUMERIC(20,8) NOT NULL,
    bucket_count          BIGINT NOT NULL,
    settled_bucket_count  BIGINT NOT NULL,
    unmapped_bucket_count BIGINT NOT NULL,
    complete              BOOLEAN NOT NULL DEFAULT FALSE,
    calculated_at         TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_profit_daily_day
    ON profit_daily_snapshots (day_start);

CREATE TABLE IF NOT EXISTS billing_sync_states (
    source                   VARCHAR(64) PRIMARY KEY,
    last_successful_end_at  TIMESTAMPTZ,
    last_attempt_at          TIMESTAMPTZ,
    last_success_at          TIMESTAMPTZ,
    status                   VARCHAR(16) NOT NULL,
    last_error               TEXT,
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);
