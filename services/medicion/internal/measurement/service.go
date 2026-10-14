package measurement

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"github.com/volt-barrio/volt-barrio/pkg/market"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidReading    = errors.New("invalid meter reading")
	ErrUnknownKey        = errors.New("unknown measurement signing key")
	ErrInvalidSignature  = errors.New("invalid measurement signature")
	ErrReadingConflict   = errors.New("reading id reused with different contents")
	ErrRateLimited       = errors.New("household measurement rate limit exceeded")
	ErrRejectionLog      = errors.New("could not persist rejected measurement attempt")
)

type SigningKey struct {
	HouseholdID string
	PublicKey   ed25519.PublicKey
}

type Store interface {
	Accept(context.Context, *voltbarrio.MeterReading, string, []byte) (bool, error)
	RecordRejected(context.Context, string, string, string) error
}

type Service struct {
	store     Store
	keys      map[string]SigningKey
	limit     int
	window    time.Duration
	mu        sync.Mutex
	household map[string]rateWindow
	now       func() time.Time
}

type rateWindow struct {
	start time.Time
	count int
}

func NewService(store Store, keys map[string]SigningKey, limit int, window time.Duration) (*Service, error) {
	if store == nil || limit < 1 || window <= 0 {
		return nil, ErrInvalidReading
	}
	copied := make(map[string]SigningKey, len(keys))
	for id, key := range keys {
		if id == "" || key.HouseholdID == "" || len(key.PublicKey) != ed25519.PublicKeySize {
			return nil, ErrInvalidReading
		}
		copied[id] = SigningKey{
			HouseholdID: key.HouseholdID,
			PublicKey:   append(ed25519.PublicKey(nil), key.PublicKey...),
		}
	}
	return &Service{
		store: store, keys: copied, limit: limit, window: window,
		household: make(map[string]rateWindow), now: time.Now,
	}, nil
}

func (s *Service) Ingest(ctx context.Context, signed *voltbarrio.SignedMeterReading) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if signed == nil || signed.Reading == nil || !validReading(signed.Reading) {
		return false, ErrInvalidReading
	}
	key, ok := s.keys[signed.KeyId]
	if !ok || key.HouseholdID != signed.Reading.HouseholdId {
		return false, s.reject(ctx, signed.KeyId, signed.Reading.ReadingId, ErrUnknownKey)
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(signed.Reading)
	if err != nil {
		return false, fmt.Errorf("marshal signed reading: %w", err)
	}
	if len(signed.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(key.PublicKey, canonical, signed.Signature) {
		return false, s.reject(ctx, signed.KeyId, signed.Reading.ReadingId, ErrInvalidSignature)
	}
	if err := s.takeToken(signed.Reading.HouseholdId); err != nil {
		return false, err
	}
	duplicate, err := s.store.Accept(ctx, signed.Reading, signed.KeyId, signed.Signature)
	if err != nil {
		return false, err
	}
	return duplicate, nil
}

func (s *Service) reject(ctx context.Context, keyID, readingID string, reason error) error {
	if err := s.store.RecordRejected(ctx, keyID, readingID, reason.Error()); err != nil {
		return errors.Join(reason, ErrRejectionLog, fmt.Errorf("record rejected measurement: %w", err))
	}
	return reason
}

func (s *Service) takeToken(householdID string) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	window := s.household[householdID]
	if now.Sub(window.start) >= s.window || now.Before(window.start) {
		window = rateWindow{start: now}
	}
	if window.count >= s.limit {
		s.household[householdID] = window
		return ErrRateLimited
	}
	window.count++
	s.household[householdID] = window
	return nil
}

func validReading(reading *voltbarrio.MeterReading) bool {
	if reading.ReadingId == "" || reading.HouseholdId == "" || reading.MeterId == "" ||
		reading.IntervalStart == nil || reading.IntervalStart.CheckValid() != nil {
		return false
	}
	return market.IsValidIntervalStart(reading.IntervalStart.AsTime())
}

func sameCanonicalReading(left, right *voltbarrio.MeterReading) bool {
	leftBytes, leftErr := (proto.MarshalOptions{Deterministic: true}).Marshal(left)
	rightBytes, rightErr := (proto.MarshalOptions{Deterministic: true}).Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}
