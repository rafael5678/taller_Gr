package measurement

import (
	"context"
	"errors"
	"sync"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"google.golang.org/protobuf/proto"
)

type MemoryStore struct {
	mu       sync.Mutex
	readings map[string]*voltbarrio.MeterReading
	rejected []RejectedAttempt
}

type RejectedAttempt struct {
	KeyID     string
	ReadingID string
	Reason    string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{readings: make(map[string]*voltbarrio.MeterReading)}
}

func (s *MemoryStore) Accept(ctx context.Context, reading *voltbarrio.MeterReading, _ string, _ []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.readings[reading.ReadingId]; exists {
		if !sameCanonicalReading(previous, reading) {
			return false, ErrReadingConflict
		}
		return true, nil
	}
	s.readings[reading.ReadingId] = proto.Clone(reading).(*voltbarrio.MeterReading)
	return false, nil
}

func (s *MemoryStore) RecordRejected(ctx context.Context, keyID, readingID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reason == "" {
		return errors.New("empty rejection reason")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = append(s.rejected, RejectedAttempt{KeyID: keyID, ReadingID: readingID, Reason: reason})
	return nil
}
