-- Persist the configured initial-history window so long backfills resume from
-- their last committed day instead of re-reading the full range after a timeout.
ALTER TABLE billing_sync_states
    ADD COLUMN IF NOT EXISTS initial_sync_started_at TIMESTAMPTZ;

ALTER TABLE billing_sync_states
    ADD COLUMN IF NOT EXISTS initial_sync_target_end_at TIMESTAMPTZ;
