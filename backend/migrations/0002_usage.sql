ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS last_usage_total numeric(20,8),
    ADD COLUMN IF NOT EXISTS last_usage_today numeric(20,8),
    ADD COLUMN IF NOT EXISTS usage_currency varchar(16),
    ADD COLUMN IF NOT EXISTS last_usage_at timestamptz;

CREATE TABLE IF NOT EXISTS usage_buckets (
    id bigserial PRIMARY KEY,
    channel_id bigint NOT NULL,
    bucket_start timestamptz NOT NULL,
    bucket_end timestamptz NOT NULL,
    resolution_seconds integer NOT NULL,
    amount numeric(20,8) NOT NULL,
    currency varchar(16) NOT NULL DEFAULT 'USD',
    source varchar(64) NOT NULL,
    quality varchar(16) NOT NULL,
    complete boolean NOT NULL DEFAULT true,
    collected_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT idx_usage_channel_start_resolution
        UNIQUE (channel_id, bucket_start, resolution_seconds)
);

CREATE INDEX IF NOT EXISTS idx_usage_buckets_channel_id ON usage_buckets (channel_id);
CREATE INDEX IF NOT EXISTS idx_usage_buckets_bucket_start ON usage_buckets (bucket_start);
