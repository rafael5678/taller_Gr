package audit

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

var ErrAuditStore = errors.New("invalid or unavailable audit store")

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrAuditStore
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Append(ctx context.Context, event Event, recordedAt time.Time) (Record, error) {
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()

	var sequenceNumber int64
	var previousHash []byte
	err = tx.QueryRowContext(ctx,
		`SELECT sequence_number, record_hash
		 FROM audit_head
		 WHERE singleton = TRUE
		 FOR UPDATE`,
	).Scan(&sequenceNumber, &previousHash)
	if err != nil {
		return Record{}, err
	}

	existing, err := scanRecord(tx.QueryRowContext(ctx,
		`SELECT sequence_number, event_id, event_type, actor_id, payload,
		        previous_hash, record_hash, recorded_at
		 FROM audit_records
		 WHERE event_id = $1`,
		event.ID,
	))
	if err == nil {
		if !sameEvent(existing.Event, event) {
			return Record{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	if sequenceNumber < 0 || sequenceNumber == math.MaxInt64 || len(previousHash) != 32 {
		return Record{}, ErrAuditStore
	}

	record := Record{
		Sequence:     uint64(sequenceNumber + 1),
		Event:        cloneEvent(event),
		PreviousHash: append([]byte(nil), previousHash...),
		RecordedAt:   normalizeRecordedAt(recordedAt),
	}
	record.RecordHash = hashRecord(record)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO audit_records
		   (sequence_number, event_id, event_type, actor_id, payload,
		    previous_hash, record_hash, recorded_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		record.Sequence,
		record.Event.ID,
		record.Event.Type,
		record.Event.ActorID,
		record.Event.Payload,
		record.PreviousHash,
		record.RecordHash,
		record.RecordedAt,
	)
	if err != nil {
		return Record{}, err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE audit_head
		 SET sequence_number = $1, record_hash = $2
		 WHERE singleton = TRUE`,
		record.Sequence,
		record.RecordHash,
	)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

func (s *PostgresStore) Records(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence_number, event_id, event_type, actor_id, payload,
		        previous_hash, record_hash, recorded_at
		 FROM audit_records
		 ORDER BY sequence_number`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(scanner rowScanner) (Record, error) {
	var sequenceNumber int64
	var record Record
	err := scanner.Scan(
		&sequenceNumber,
		&record.Event.ID,
		&record.Event.Type,
		&record.Event.ActorID,
		&record.Event.Payload,
		&record.PreviousHash,
		&record.RecordHash,
		&record.RecordedAt,
	)
	if err != nil {
		return Record{}, err
	}
	if sequenceNumber < 1 {
		return Record{}, ErrAuditStore
	}
	record.Sequence = uint64(sequenceNumber)
	record = cloneRecord(record)
	return record, nil
}
