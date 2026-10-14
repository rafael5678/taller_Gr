package stability

import (
	"context"
	"errors"
	"math/bits"
	"sort"
	"strings"
	"sync"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

var (
	ErrInvalidBatch       = errors.New("invalid clearing batch")
	ErrIdempotencyConflict = errors.New("batch id reused with different request")
	ErrIntervalAlreadyValidated = errors.New("interval already has a validated batch")
)

type batchRecord struct {
	request market.ClearingBatch
	result  market.ValidatedBatch
}

type Service struct {
	mu                  sync.Mutex
	transformerLimitWh  uint64
	batches             map[string]batchRecord
	batchByInterval     map[int64]string
}

func NewService(transformerLimitWh uint64) *Service {
	return &Service{
		transformerLimitWh: transformerLimitWh,
		batches:            make(map[string]batchRecord),
		batchByInterval:    make(map[int64]string),
	}
}

func (s *Service) ValidateBatch(ctx context.Context, batch market.ClearingBatch) (market.ValidatedBatch, error) {
	if err := ctx.Err(); err != nil {
		return market.ValidatedBatch{}, err
	}
	proposedEnergyWh, err := validateBatch(batch)
	if err != nil {
		return market.ValidatedBatch{}, err
	}

	batch.IntervalStart = batch.IntervalStart.UTC()
	batch = cloneBatch(batch)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return market.ValidatedBatch{}, err
	}
	if previous, ok := s.batches[batch.ID]; ok {
		if !sameBatch(previous.request, batch) {
			return market.ValidatedBatch{}, ErrIdempotencyConflict
		}
		return cloneValidatedBatch(previous.result), nil
	}
	intervalKey := batch.IntervalStart.Unix()
	if _, exists := s.batchByInterval[intervalKey]; exists {
		return market.ValidatedBatch{}, ErrIntervalAlreadyValidated
	}

	result := market.ValidatedBatch{
		ID:                             batch.ID,
		IntervalStart:                  batch.IntervalStart,
		TransformerLimitWh:             s.transformerLimitWh,
		ProposedEnergyWh:               proposedEnergyWh,
		ClosingPriceMicrocreditsPerKWh: batch.ClosingPriceMicrocreditsPerKWh,
		Trades:                         make([]market.ValidatedTrade, len(batch.Trades)),
	}
	acceptedEnergyWh := proposedEnergyWh
	if proposedEnergyWh > s.transformerLimitWh {
		allocations, err := prorate(batch.Trades, proposedEnergyWh, s.transformerLimitWh)
		if err != nil {
			return market.ValidatedBatch{}, err
		}
		acceptedEnergyWh = s.transformerLimitWh
		for i, trade := range batch.Trades {
			result.Trades[i] = market.ValidatedTrade{
				TradeID:          trade.ID,
				ProposedEnergyWh: trade.EnergyWh,
				AcceptedEnergyWh: allocations[i],
			}
		}
	} else {
		for i, trade := range batch.Trades {
			result.Trades[i] = market.ValidatedTrade{
				TradeID:          trade.ID,
				ProposedEnergyWh: trade.EnergyWh,
				AcceptedEnergyWh: trade.EnergyWh,
			}
		}
	}
	result.AcceptedEnergyWh = acceptedEnergyWh
	result.RecortadoPorRed = acceptedEnergyWh < proposedEnergyWh

	s.batches[batch.ID] = batchRecord{
		request: batch,
		result:  cloneValidatedBatch(result),
	}
	s.batchByInterval[intervalKey] = batch.ID
	return result, nil
}

func validateBatch(batch market.ClearingBatch) (uint64, error) {
	if strings.TrimSpace(batch.ID) == "" || !market.IsValidIntervalStart(batch.IntervalStart) {
		return 0, ErrInvalidBatch
	}

	var total uint64
	seenTradeIDs := make(map[string]struct{}, len(batch.Trades))
	for _, trade := range batch.Trades {
		if strings.TrimSpace(trade.ID) == "" ||
			strings.TrimSpace(trade.BuyerHouseholdID) == "" ||
			strings.TrimSpace(trade.SellerHouseholdID) == "" ||
			trade.EnergyWh == 0 {
			return 0, ErrInvalidBatch
		}
		if _, exists := seenTradeIDs[trade.ID]; exists {
			return 0, ErrInvalidBatch
		}
		seenTradeIDs[trade.ID] = struct{}{}
		if ^uint64(0)-total < trade.EnergyWh {
			return 0, ErrInvalidBatch
		}
		total += trade.EnergyWh
	}
	return total, nil
}

func prorate(trades []market.Trade, total, limit uint64) ([]uint64, error) {
	if total == 0 || limit >= total {
		return nil, ErrInvalidBatch
	}
	type allocation struct {
		index     int
		remainder uint64
		tradeID   string
	}

	accepted := make([]uint64, len(trades))
	remainders := make([]allocation, len(trades))
	var allocated uint64
	for i, trade := range trades {
		high, low := bits.Mul64(limit, trade.EnergyWh)
		if high >= total {
			return nil, ErrInvalidBatch
		}
		quotient, remainder := bits.Div64(high, low, total)
		accepted[i] = quotient
		allocated += quotient
		remainders[i] = allocation{index: i, remainder: remainder, tradeID: trade.ID}
	}

	unitsLeft := limit - allocated
	if unitsLeft > uint64(len(remainders)) {
		return nil, ErrInvalidBatch
	}
	sort.Slice(remainders, func(i, j int) bool {
		if remainders[i].remainder != remainders[j].remainder {
			return remainders[i].remainder > remainders[j].remainder
		}
		return remainders[i].tradeID < remainders[j].tradeID
	})
	for i := uint64(0); i < unitsLeft; i++ {
		accepted[remainders[int(i)].index]++
	}
	var acceptedTotal uint64
	for i, energyWh := range accepted {
		if energyWh > trades[i].EnergyWh || ^uint64(0)-acceptedTotal < energyWh {
			return nil, ErrInvalidBatch
		}
		acceptedTotal += energyWh
	}
	if acceptedTotal != limit {
		return nil, ErrInvalidBatch
	}
	return accepted, nil
}

func sameBatch(left, right market.ClearingBatch) bool {
	if left.ID != right.ID ||
		!left.IntervalStart.Equal(right.IntervalStart) ||
		left.ClosingPriceMicrocreditsPerKWh != right.ClosingPriceMicrocreditsPerKWh ||
		len(left.Trades) != len(right.Trades) {
		return false
	}
	for i := range left.Trades {
		if left.Trades[i] != right.Trades[i] {
			return false
		}
	}
	return true
}

func cloneBatch(batch market.ClearingBatch) market.ClearingBatch {
	batch.Trades = append([]market.Trade(nil), batch.Trades...)
	return batch
}

func cloneValidatedBatch(batch market.ValidatedBatch) market.ValidatedBatch {
	batch.Trades = append([]market.ValidatedTrade(nil), batch.Trades...)
	return batch
}
