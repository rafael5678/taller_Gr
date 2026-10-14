package measurement

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	_ "github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/proto"
)

var ErrMeasurementStore = errors.New("invalid or unavailable measurement store")

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrMeasurementStore
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Accept(ctx context.Context, reading *voltbarrio.MeterReading, keyID string, signature []byte) (bool, error) {
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(reading)
	if err != nil {
		return false, err
	}
	var inserted string
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO measurement_readings
		   (reading_id, household_id, meter_id, interval_start,
		    produced_energy_wh, consumed_energy_wh, key_id, signature, canonical_reading)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (reading_id) DO NOTHING
		 RETURNING reading_id`,
		reading.ReadingId, reading.HouseholdId, reading.MeterId, reading.IntervalStart.AsTime(),
		strconv.FormatUint(reading.ProducedEnergyWh, 10), strconv.FormatUint(reading.ConsumedEnergyWh, 10),
		keyID, signature, canonical,
	).Scan(&inserted)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var previous []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT canonical_reading FROM measurement_readings WHERE reading_id = $1`,
		reading.ReadingId,
	).Scan(&previous); err != nil {
		return false, err
	}
	if !bytes.Equal(previous, canonical) {
		return false, ErrReadingConflict
	}
	return true, nil
}

func (s *PostgresStore) RecordRejected(ctx context.Context, keyID, readingID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO measurement_rejected_attempts (key_id, reading_id, reason)
		 VALUES ($1, $2, $3)`,
		keyID, readingID, reason,
	)
	return err
}
