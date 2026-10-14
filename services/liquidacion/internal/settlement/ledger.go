package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

var (
	ErrInvalidSettlement   = errors.New("invalid settlement")
	ErrSettlementNotFound  = errors.New("settlement not found")
	ErrAlreadyCompensated  = errors.New("settlement already compensated")
	ErrEntryIDConflict     = errors.New("ledger entry id already exists")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
)

type State uint8

const (
	StateUnspecified State = iota
	StatePosted
	StateCompensated
)

type Posting struct {
	ID                         string
	AccountID                  string
	SignedAmountMicrocredits   int64
}

type Settlement struct {
	ID            string
	AuctionID     string
	IntervalStart time.Time
	State         State
	Postings      []Posting
}

type Compensation struct {
	ID               string
	SettlementID     string
	Reason           string
	ReversalPostings []Posting
}

type Store interface {
	PostSettlement(context.Context, Settlement) (Settlement, error)
	Compensate(context.Context, Compensation) (Compensation, error)
	GetSettlement(context.Context, string) (Settlement, error)
	GetCompensation(context.Context, string) (Compensation, bool, error)
	LedgerEntries(context.Context, string) ([]Posting, error)
}

type MemoryStore struct {
	mu                  sync.RWMutex
	settlements         map[string]Settlement
	settlementByAuction map[string]string
	settlementByTime    map[int64]string
	compensations       map[string]Compensation
	compensationBySettle map[string]string
	entryIDs            map[string]string
	entries             map[string][]Posting
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		settlements:          make(map[string]Settlement),
		settlementByAuction:  make(map[string]string),
		settlementByTime:     make(map[int64]string),
		compensations:        make(map[string]Compensation),
		compensationBySettle: make(map[string]string),
		entryIDs:             make(map[string]string),
		entries:              make(map[string][]Posting),
	}
}

func (s *MemoryStore) PostSettlement(ctx context.Context, settlement Settlement) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	settlement, err := normalizeSettlement(settlement)
	if err != nil {
		return Settlement{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	if previous, ok := s.settlements[settlement.ID]; ok {
		if !sameSettlement(previous, settlement) {
			return Settlement{}, ErrIdempotencyConflict
		}
		return cloneSettlement(previous), nil
	}
	if _, ok := s.settlementByAuction[settlement.AuctionID]; ok {
		return Settlement{}, ErrInvalidSettlement
	}
	if _, ok := s.settlementByTime[settlement.IntervalStart.Unix()]; ok {
		return Settlement{}, ErrInvalidSettlement
	}
	for _, posting := range settlement.Postings {
		if _, exists := s.entryIDs[posting.ID]; exists {
			return Settlement{}, ErrEntryIDConflict
		}
	}

	settlement.State = StatePosted
	s.settlements[settlement.ID] = cloneSettlement(settlement)
	s.settlementByAuction[settlement.AuctionID] = settlement.ID
	s.settlementByTime[settlement.IntervalStart.Unix()] = settlement.ID
	for _, posting := range settlement.Postings {
		s.entryIDs[posting.ID] = settlement.ID
		s.entries[settlement.ID] = append(s.entries[settlement.ID], posting)
	}
	return cloneSettlement(settlement), nil
}

func (s *MemoryStore) Compensate(ctx context.Context, compensation Compensation) (Compensation, error) {
	if err := ctx.Err(); err != nil {
		return Compensation{}, err
	}
	if strings.TrimSpace(compensation.ID) == "" ||
		strings.TrimSpace(compensation.SettlementID) == "" ||
		strings.TrimSpace(compensation.Reason) == "" {
		return Compensation{}, ErrInvalidSettlement
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Compensation{}, err
	}
	if existingID, ok := s.compensationBySettle[compensation.SettlementID]; ok {
		previous := s.compensations[existingID]
		if previous.ID != compensation.ID || previous.Reason != compensation.Reason {
			return Compensation{}, ErrAlreadyCompensated
		}
		return cloneCompensation(previous), nil
	}
	settlement, ok := s.settlements[compensation.SettlementID]
	if !ok {
		return Compensation{}, ErrSettlementNotFound
	}
	if settlement.State != StatePosted {
		return Compensation{}, ErrAlreadyCompensated
	}
	if _, exists := s.compensations[compensation.ID]; exists {
		return Compensation{}, ErrIdempotencyConflict
	}

	reversals := make([]Posting, len(settlement.Postings))
	for i, posting := range settlement.Postings {
		reversals[i] = Posting{
			ID:                       reversalEntryID(compensation.ID, posting.ID),
			AccountID:                posting.AccountID,
			SignedAmountMicrocredits: -posting.SignedAmountMicrocredits,
		}
		if _, exists := s.entryIDs[reversals[i].ID]; exists {
			return Compensation{}, ErrEntryIDConflict
		}
	}
	compensation.ReversalPostings = reversals
	settlement.State = StateCompensated
	s.settlements[settlement.ID] = settlement
	s.compensationBySettle[settlement.ID] = compensation.ID
	s.compensations[compensation.ID] = cloneCompensation(compensation)
	for _, posting := range reversals {
		s.entryIDs[posting.ID] = settlement.ID
		s.entries[settlement.ID] = append(s.entries[settlement.ID], posting)
	}
	return cloneCompensation(compensation), nil
}

func (s *MemoryStore) GetSettlement(ctx context.Context, settlementID string) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	settlement, ok := s.settlements[settlementID]
	if !ok {
		return Settlement{}, ErrSettlementNotFound
	}
	return cloneSettlement(settlement), nil
}

func (s *MemoryStore) GetCompensation(ctx context.Context, settlementID string) (Compensation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Compensation{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.compensationBySettle[settlementID]
	if !ok {
		return Compensation{}, false, nil
	}
	return cloneCompensation(s.compensations[id]), true, nil
}

func (s *MemoryStore) LedgerEntries(ctx context.Context, settlementID string) ([]Posting, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.settlements[settlementID]; !ok {
		return nil, ErrSettlementNotFound
	}
	return append([]Posting(nil), s.entries[settlementID]...), nil
}

func normalizeSettlement(settlement Settlement) (Settlement, error) {
	if strings.TrimSpace(settlement.ID) == "" ||
		strings.TrimSpace(settlement.AuctionID) == "" ||
		!market.IsValidIntervalStart(settlement.IntervalStart) ||
		!isBalanced(settlement.Postings) {
		return Settlement{}, ErrInvalidSettlement
	}
	settlement.IntervalStart = settlement.IntervalStart.UTC()
	settlement.State = StateUnspecified
	settlement.Postings = append([]Posting(nil), settlement.Postings...)
	sort.Slice(settlement.Postings, func(i, j int) bool {
		return settlement.Postings[i].ID < settlement.Postings[j].ID
	})
	return settlement, nil
}

func isBalanced(postings []Posting) bool {
	if len(postings) < 2 {
		return false
	}
	total := new(big.Int)
	seenIDs := make(map[string]struct{}, len(postings))
	for _, posting := range postings {
		if strings.TrimSpace(posting.ID) == "" ||
			strings.TrimSpace(posting.AccountID) == "" ||
			posting.SignedAmountMicrocredits == 0 ||
			posting.SignedAmountMicrocredits == -1<<63 {
			return false
		}
		if _, exists := seenIDs[posting.ID]; exists {
			return false
		}
		seenIDs[posting.ID] = struct{}{}
		total.Add(total, big.NewInt(posting.SignedAmountMicrocredits))
	}
	return total.Sign() == 0
}

func sameSettlement(left, right Settlement) bool {
	if left.ID != right.ID ||
		left.AuctionID != right.AuctionID ||
		!left.IntervalStart.Equal(right.IntervalStart) ||
		len(left.Postings) != len(right.Postings) {
		return false
	}
	for i := range left.Postings {
		if left.Postings[i] != right.Postings[i] {
			return false
		}
	}
	return true
}

func cloneSettlement(settlement Settlement) Settlement {
	settlement.Postings = append([]Posting(nil), settlement.Postings...)
	return settlement
}

func cloneCompensation(compensation Compensation) Compensation {
	compensation.ReversalPostings = append([]Posting(nil), compensation.ReversalPostings...)
	return compensation
}

func reversalEntryID(compensationID, entryID string) string {
	hash := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(compensationID)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(compensationID))
	binary.BigEndian.PutUint64(size[:], uint64(len(entryID)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(entryID))
	return "reversal-" + hex.EncodeToString(hash.Sum(nil))
}
