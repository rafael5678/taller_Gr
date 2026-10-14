CREATE TABLE IF NOT EXISTS measurement_readings (
    reading_id TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    meter_id TEXT NOT NULL,
    interval_start TIMESTAMPTZ NOT NULL,
    produced_energy_wh NUMERIC(20, 0) NOT NULL CHECK (produced_energy_wh >= 0),
    consumed_energy_wh NUMERIC(20, 0) NOT NULL CHECK (consumed_energy_wh >= 0),
    key_id TEXT NOT NULL,
    signature BYTEA NOT NULL,
    canonical_reading BYTEA NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS measurement_readings_interval_idx
    ON measurement_readings (interval_start, household_id);

CREATE TABLE IF NOT EXISTS measurement_rejected_attempts (
    attempt_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key_id TEXT NOT NULL,
    reading_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
