CREATE TABLE IF NOT EXISTS settlements (
    settlement_id TEXT PRIMARY KEY,
    auction_id TEXT NOT NULL UNIQUE,
    interval_start TIMESTAMPTZ NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK (state IN ('posted', 'compensated')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ledger_compensations (
    compensation_id TEXT PRIMARY KEY,
    settlement_id TEXT NOT NULL UNIQUE REFERENCES settlements(settlement_id),
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    entry_id TEXT PRIMARY KEY,
    settlement_id TEXT NOT NULL REFERENCES settlements(settlement_id),
    compensation_id TEXT REFERENCES ledger_compensations(compensation_id),
    account_id TEXT NOT NULL,
    signed_amount_microcredits BIGINT NOT NULL CHECK (signed_amount_microcredits <> 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ledger_entries_settlement_idx
    ON ledger_entries (settlement_id, entry_id);

CREATE OR REPLACE FUNCTION enforce_balanced_ledger()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    balance NUMERIC;
    entry_count BIGINT;
BEGIN
    SELECT COALESCE(SUM(signed_amount_microcredits), 0), COUNT(*)
      INTO balance, entry_count
      FROM ledger_entries
     WHERE settlement_id = NEW.settlement_id;
    IF balance <> 0 OR entry_count < 2 THEN
        RAISE EXCEPTION 'settlement % must have at least two balanced ledger entries', NEW.settlement_id;
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS settlements_balance_check ON settlements;
CREATE CONSTRAINT TRIGGER settlements_balance_check
AFTER INSERT ON settlements
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_balanced_ledger();

DROP TRIGGER IF EXISTS ledger_entries_balance_check ON ledger_entries;
CREATE CONSTRAINT TRIGGER ledger_entries_balance_check
AFTER INSERT ON ledger_entries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_balanced_ledger();

CREATE OR REPLACE FUNCTION reject_ledger_entry_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'ledger entries are append-only';
END;
$$;

DROP TRIGGER IF EXISTS ledger_entries_append_only ON ledger_entries;
CREATE TRIGGER ledger_entries_append_only
BEFORE UPDATE OR DELETE ON ledger_entries
FOR EACH ROW
EXECUTE FUNCTION reject_ledger_entry_mutation();
