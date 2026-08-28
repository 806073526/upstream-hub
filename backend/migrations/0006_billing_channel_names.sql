-- Billing upgrade for channel-name snapshots and charged quota audit values.
-- The server's GORM AutoMigrate also adds these columns on startup; this file
-- records the equivalent production migration for existing installations.
ALTER TABLE newapi_billing_buckets
    ADD COLUMN IF NOT EXISTS new_api_channel_name VARCHAR(256);

ALTER TABLE newapi_billing_events
    ADD COLUMN IF NOT EXISTS channel_name VARCHAR(256);

ALTER TABLE profit_buckets
    ADD COLUMN IF NOT EXISTS new_api_channel_name VARCHAR(256);

ALTER TABLE profit_buckets
    ADD COLUMN IF NOT EXISTS charged_usd NUMERIC(20,8) NOT NULL DEFAULT 0;
