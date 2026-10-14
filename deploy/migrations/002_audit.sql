CREATE TABLE IF NOT EXISTS audit_head (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    sequence_number BIGINT NOT NULL CHECK (sequence_number >= 0),
    record_hash BYTEA NOT NULL CHECK (octet_length(record_hash) = 32)
);

INSERT INTO audit_head (singleton, sequence_number, record_hash)
VALUES (TRUE, 0, decode(repeat('00', 32), 'hex'))
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS audit_records (
    sequence_number BIGINT PRIMARY KEY CHECK (sequence_number > 0),
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    payload BYTEA NOT NULL,
    previous_hash BYTEA NOT NULL CHECK (octet_length(previous_hash) = 32),
    record_hash BYTEA NOT NULL CHECK (octet_length(record_hash) = 32),
    recorded_at TIMESTAMPTZ NOT NULL
);

CREATE OR REPLACE FUNCTION reject_audit_record_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit records are append-only';
END;
$$;

DROP TRIGGER IF EXISTS audit_records_append_only ON audit_records;
CREATE TRIGGER audit_records_append_only
BEFORE UPDATE OR DELETE ON audit_records
FOR EACH ROW
EXECUTE FUNCTION reject_audit_record_mutation();
