package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidEvent       = errors.New("invalid audit event")
	ErrIdempotencyConflict = errors.New("event id reused with different contents")
)

type Event struct {
	ID        string
	Type      string
	ActorID   string
	Payload   []byte
}

type Record struct {
	Sequence    uint64
	Event       Event
	PreviousHash []byte
	RecordHash   []byte
	RecordedAt   time.Time
}

type Verification struct {
	Valid                 bool
	RecordsChecked        uint64
	FirstInvalidSequence  uint64
	Error                 string
	HashAlgorithm         string
}

type Store interface {
	Append(context.Context, Event, time.Time) (Record, error)
	Records(context.Context) ([]Record, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

type MemoryStore struct {
	mu      sync.RWMutex
	records []Record
	events  map[string]Record
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, ErrInvalidEvent
	}
	return &Service{store: store, now: time.Now}, nil
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: make(map[string]Record)}
}

func (s *Service) Append(ctx context.Context, event Event) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	return s.store.Append(ctx, cloneEvent(event), s.now().UTC())
}

func (s *Service) Verify(ctx context.Context) (Verification, error) {
	records, err := s.store.Records(ctx)
	if err != nil {
		return Verification{}, err
	}
	result := Verification{
		Valid:         true,
		HashAlgorithm: "SHA-256",
	}
	previousHash := make([]byte, sha256.Size)
	for i, record := range records {
		expectedSequence := uint64(i + 1)
		result.RecordsChecked++
		if record.Sequence != expectedSequence ||
			!bytes.Equal(record.PreviousHash, previousHash) ||
			!bytes.Equal(record.RecordHash, hashRecord(record)) {
			result.Valid = false
			result.FirstInvalidSequence = expectedSequence
			result.Error = "hash chain or sequence mismatch"
			return result, nil
		}
		previousHash = append(previousHash[:0], record.RecordHash...)
	}
	return result, nil
}

func (s *MemoryStore) Append(ctx context.Context, event Event, recordedAt time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if previous, ok := s.events[event.ID]; ok {
		if !sameEvent(previous.Event, event) {
			return Record{}, ErrIdempotencyConflict
		}
		return cloneRecord(previous), nil
	}

	previousHash := make([]byte, sha256.Size)
	if len(s.records) > 0 {
		previousHash = append(previousHash[:0], s.records[len(s.records)-1].RecordHash...)
	}
	record := Record{
		Sequence:     uint64(len(s.records) + 1),
		Event:        cloneEvent(event),
		PreviousHash: previousHash,
		RecordedAt:   normalizeRecordedAt(recordedAt),
	}
	record.RecordHash = hashRecord(record)
	s.records = append(s.records, cloneRecord(record))
	s.events[event.ID] = cloneRecord(record)
	return cloneRecord(record), nil
}

func normalizeRecordedAt(recordedAt time.Time) time.Time {
	return recordedAt.UTC().Truncate(time.Microsecond)
}

func (s *MemoryStore) Records(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Record, len(s.records))
	for i, record := range s.records {
		result[i] = cloneRecord(record)
	}
	return result, nil
}

func validateEvent(event Event) error {
	if strings.TrimSpace(event.ID) == "" ||
		strings.TrimSpace(event.Type) == "" ||
		strings.TrimSpace(event.ActorID) == "" {
		return ErrInvalidEvent
	}
	return nil
}

func sameEvent(left, right Event) bool {
	return left.ID == right.ID &&
		left.Type == right.Type &&
		left.ActorID == right.ActorID &&
		bytes.Equal(left.Payload, right.Payload)
}

func cloneEvent(event Event) Event {
	event.Payload = append([]byte(nil), event.Payload...)
	return event
}

func cloneRecord(record Record) Record {
	record.Event = cloneEvent(record.Event)
	record.PreviousHash = append([]byte(nil), record.PreviousHash...)
	record.RecordHash = append([]byte(nil), record.RecordHash...)
	return record
}

func hashRecord(record Record) []byte {
	hash := sha256.New()
	writeUint64(hash, record.Sequence)
	writeField(hash, []byte(record.Event.ID))
	writeField(hash, []byte(record.Event.Type))
	writeField(hash, []byte(record.Event.ActorID))
	writeField(hash, record.Event.Payload)
	writeUint64(hash, uint64(record.RecordedAt.UTC().Unix()))
	writeUint64(hash, uint64(record.RecordedAt.UTC().Nanosecond()))
	writeField(hash, record.PreviousHash)
	return hash.Sum(nil)
}

func writeUint64(hash interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = hash.Write(encoded[:])
}

func writeField(hash interface{ Write([]byte) (int, error) }, value []byte) {
	writeUint64(hash, uint64(len(value)))
	_, _ = hash.Write(value)
}
