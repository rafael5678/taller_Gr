package settlement

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var ErrLedgerStore = errors.New("invalid or unavailable ledger store")

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrLedgerStore
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) PostSettlement(ctx context.Context, settlement Settlement) (Settlement, error) {
	settlement, err := normalizeSettlement(settlement)
	if err != nil {
		return Settlement{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settlement{}, err
	}
	defer tx.Rollback()

	var insertedID string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO settlements
		   (settlement_id, auction_id, interval_start, state)
		 VALUES ($1, $2, $3, 'posted')
		 ON CONFLICT (settlement_id) DO NOTHING
		 RETURNING settlement_id`,
		settlement.ID,
		settlement.AuctionID,
		settlement.IntervalStart,
	).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return Settlement{}, err
		}
		previous, getErr := s.GetSettlement(ctx, settlement.ID)
		if getErr != nil {
			return Settlement{}, getErr
		}
		if !sameSettlement(previous, settlement) {
			return Settlement{}, ErrIdempotencyConflict
		}
		return previous, nil
	}
	if err != nil {
		return Settlement{}, err
	}

	for _, posting := range settlement.Postings {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO ledger_entries
			   (entry_id, settlement_id, compensation_id, account_id, signed_amount_microcredits)
			 VALUES ($1, $2, NULL, $3, $4)`,
			posting.ID,
			settlement.ID,
			posting.AccountID,
			posting.SignedAmountMicrocredits,
		)
		if err != nil {
			return Settlement{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Settlement{}, err
	}
	settlement.State = StatePosted
	return cloneSettlement(settlement), nil
}

func (s *PostgresStore) Compensate(ctx context.Context, compensation Compensation) (Compensation, error) {
	if strings.TrimSpace(compensation.ID) == "" ||
		strings.TrimSpace(compensation.SettlementID) == "" ||
		strings.TrimSpace(compensation.Reason) == "" {
		return Compensation{}, ErrInvalidSettlement
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Compensation{}, err
	}
	defer tx.Rollback()

	settlement, err := scanSettlementRow(tx.QueryRowContext(ctx,
		`SELECT settlement_id, auction_id, interval_start, state
		 FROM settlements
		 WHERE settlement_id = $1
		 FOR UPDATE`,
		compensation.SettlementID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Compensation{}, ErrSettlementNotFound
	}
	if err != nil {
		return Compensation{}, err
	}

	var existingID, existingReason string
	err = tx.QueryRowContext(ctx,
		`SELECT compensation_id, reason
		 FROM ledger_compensations
		 WHERE settlement_id = $1`,
		compensation.SettlementID,
	).Scan(&existingID, &existingReason)
	if err == nil {
		if existingID != compensation.ID || existingReason != compensation.Reason {
			return Compensation{}, ErrAlreadyCompensated
		}
		compensation.ReversalPostings, err = queryPostings(ctx, tx,
			`SELECT entry_id, account_id, signed_amount_microcredits
			 FROM ledger_entries
			 WHERE compensation_id = $1
			 ORDER BY entry_id`,
			existingID,
		)
		if err != nil {
			return Compensation{}, err
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return Compensation{}, err
		}
		return cloneCompensation(compensation), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Compensation{}, err
	}
	if settlement.State != StatePosted {
		return Compensation{}, ErrAlreadyCompensated
	}

	originals, err := queryPostings(ctx, tx,
		`SELECT entry_id, account_id, signed_amount_microcredits
		 FROM ledger_entries
		 WHERE settlement_id = $1 AND compensation_id IS NULL
		 ORDER BY entry_id`,
		compensation.SettlementID,
	)
	if err != nil {
		return Compensation{}, err
	}
	if len(originals) < 2 {
		return Compensation{}, ErrInvalidSettlement
	}
	reversals := make([]Posting, len(originals))
	for i, original := range originals {
		reversals[i] = Posting{
			ID:                     reversalEntryID(compensation.ID, original.ID),
			AccountID:              original.AccountID,
			SignedAmountMicrocredits: -original.SignedAmountMicrocredits,
		}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO ledger_compensations (compensation_id, settlement_id, reason)
		 VALUES ($1, $2, $3)`,
		compensation.ID,
		compensation.SettlementID,
		compensation.Reason,
	)
	if err != nil {
		return Compensation{}, err
	}
	for _, posting := range reversals {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO ledger_entries
			   (entry_id, settlement_id, compensation_id, account_id, signed_amount_microcredits)
			 VALUES ($1, $2, $3, $4, $5)`,
			posting.ID,
			compensation.SettlementID,
			compensation.ID,
			posting.AccountID,
			posting.SignedAmountMicrocredits,
		)
		if err != nil {
			return Compensation{}, err
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE settlements SET state = 'compensated' WHERE settlement_id = $1`,
		compensation.SettlementID,
	)
	if err != nil {
		return Compensation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Compensation{}, err
	}
	compensation.ReversalPostings = reversals
	return cloneCompensation(compensation), nil
}

func (s *PostgresStore) GetSettlement(ctx context.Context, settlementID string) (Settlement, error) {
	settlement, err := scanSettlementRow(s.db.QueryRowContext(ctx,
		`SELECT settlement_id, auction_id, interval_start, state
		 FROM settlements
		 WHERE settlement_id = $1`,
		settlementID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Settlement{}, ErrSettlementNotFound
	}
	if err != nil {
		return Settlement{}, err
	}
	settlement.Postings, err = queryPostings(ctx, s.db,
		`SELECT entry_id, account_id, signed_amount_microcredits
		 FROM ledger_entries
		 WHERE settlement_id = $1 AND compensation_id IS NULL
		 ORDER BY entry_id`,
		settlementID,
	)
	if err != nil {
		return Settlement{}, err
	}
	return settlement, nil
}

func (s *PostgresStore) GetCompensation(ctx context.Context, settlementID string) (Compensation, bool, error) {
	var compensation Compensation
	err := s.db.QueryRowContext(ctx,
		`SELECT compensation_id, settlement_id, reason
		 FROM ledger_compensations
		 WHERE settlement_id = $1`,
		settlementID,
	).Scan(&compensation.ID, &compensation.SettlementID, &compensation.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return Compensation{}, false, nil
	}
	if err != nil {
		return Compensation{}, false, err
	}
	compensation.ReversalPostings, err = queryPostings(ctx, s.db,
		`SELECT entry_id, account_id, signed_amount_microcredits
		 FROM ledger_entries
		 WHERE compensation_id = $1
		 ORDER BY entry_id`,
		compensation.ID,
	)
	if err != nil {
		return Compensation{}, false, err
	}
	return compensation, true, nil
}

func (s *PostgresStore) LedgerEntries(ctx context.Context, settlementID string) ([]Posting, error) {
	if _, err := s.GetSettlement(ctx, settlementID); err != nil {
		return nil, err
	}
	return queryPostings(ctx, s.db,
		`SELECT entry_id, account_id, signed_amount_microcredits
		 FROM ledger_entries
		 WHERE settlement_id = $1
		 ORDER BY entry_id`,
		settlementID,
	)
}

type settlementRowScanner interface {
	Scan(...any) error
}

type postingQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func scanSettlementRow(scanner settlementRowScanner) (Settlement, error) {
	var settlement Settlement
	var state string
	if err := scanner.Scan(
		&settlement.ID,
		&settlement.AuctionID,
		&settlement.IntervalStart,
		&state,
	); err != nil {
		return Settlement{}, err
	}
	switch state {
	case "posted":
		settlement.State = StatePosted
	case "compensated":
		settlement.State = StateCompensated
	default:
		return Settlement{}, ErrLedgerStore
	}
	return settlement, nil
}

func queryPostings(ctx context.Context, queryer postingQueryer, query string, args ...any) ([]Posting, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var postings []Posting
	for rows.Next() {
		var posting Posting
		if err := rows.Scan(&posting.ID, &posting.AccountID, &posting.SignedAmountMicrocredits); err != nil {
			return nil, err
		}
		postings = append(postings, posting)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return postings, nil
}
