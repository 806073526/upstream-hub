-- Mark the one-time NewAPI initialization-history backfill. Existing rows
-- remain NULL so the next sync can repair installations that previously only
-- collected the configured lookback window.
ALTER TABLE billing_sync_states
    ADD COLUMN IF NOT EXISTS initial_sync_completed_at TIMESTAMPTZ;
