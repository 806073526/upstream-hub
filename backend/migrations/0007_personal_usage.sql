-- Root-user consumption is kept in a separate ledger so it can be deducted
-- from gross profit without inflating sales.
CREATE TABLE IF NOT EXISTS newapi_personal_usage_buckets (
    id                    BIGSERIAL PRIMARY KEY,
    fact_key              VARCHAR(128) NOT NULL UNIQUE,
    bucket_start          TIMESTAMPTZ NOT NULL,
    bucket_end            TIMESTAMPTZ NOT NULL,
    resolution_seconds    INTEGER NOT NULL,
    consume_quota         BIGINT NOT NULL,
    refund_quota          BIGINT NOT NULL,
    net_quota             BIGINT NOT NULL,
    event_count           BIGINT NOT NULL,
    quota_per_unit        NUMERIC(20,8) NOT NULL,
    credit_usd_per_cny    NUMERIC(20,8) NOT NULL,
    personal_usage_cny    NUMERIC(20,8) NOT NULL,
    complete              BOOLEAN NOT NULL DEFAULT FALSE,
    collected_at          TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_newapi_personal_usage_window
    ON newapi_personal_usage_buckets (bucket_start, resolution_seconds);
