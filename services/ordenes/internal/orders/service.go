package orders

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

var (
	ErrInvalidOrder       = errors.New("invalid order")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
	ErrOrderNotFound      = errors.New("order not found")
	ErrOrderNotOpen       = errors.New("order is not open")
	ErrIntervalClosed     = errors.New("order interval is already closed")
	ErrAuctionAlreadyRun  = errors.New("auction already committed for interval")
)

type cancellationRecord struct {
	orderID string
	result  market.Order
}

type Service struct {
	mu                 sync.RWMutex
	orders             map[string]market.Order
	cancellations      map[string]cancellationRecord
	auctions           map[string]market.AuctionResult
	auctionByInterval  map[int64]string
}

func NewService() *Service {
	return &Service{
		orders:            make(map[string]market.Order),
		cancellations:     make(map[string]cancellationRecord),
		auctions:          make(map[string]market.AuctionResult),
		auctionByInterval: make(map[int64]string),
	}
}

func (s *Service) Submit(ctx context.Context, request market.PlaceOrderRequest) (market.Order, error) {
	if err := ctx.Err(); err != nil {
		return market.Order{}, err
	}
	if err := validateRequest(request); err != nil {
		return market.Order{}, err
	}

	request.IntervalStart = request.IntervalStart.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.orders[request.ID]; ok {
		if !sameRequest(existing, request) {
			return market.Order{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	if _, closed := s.auctionByInterval[request.IntervalStart.Unix()]; closed {
		return market.Order{}, ErrIntervalClosed
	}

	now := time.Now().UTC()
	order := market.Order{
		ID:                           request.ID,
		HouseholdID:                  request.HouseholdID,
		Side:                         request.Side,
		IntervalStart:                request.IntervalStart,
		EnergyWh:                     request.EnergyWh,
		RemainingEnergyWh:            request.EnergyWh,
		LimitPriceMicrocreditsPerKWh: request.LimitPriceMicrocreditsPerKWh,
		CreatedAt:                    now,
		State:                        market.StateOpen,
	}
	s.orders[order.ID] = order
	return order, nil
}

func (s *Service) Cancel(ctx context.Context, cancellationID, orderID string) (market.Order, error) {
	if err := ctx.Err(); err != nil {
		return market.Order{}, err
	}
	if strings.TrimSpace(cancellationID) == "" || strings.TrimSpace(orderID) == "" {
		return market.Order{}, ErrInvalidOrder
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if previous, ok := s.cancellations[cancellationID]; ok {
		if previous.orderID != orderID {
			return market.Order{}, ErrIdempotencyConflict
		}
		return previous.result, nil
	}

	order, ok := s.orders[orderID]
	if !ok {
		return market.Order{}, ErrOrderNotFound
	}
	if order.State == market.StateFilled {
		return market.Order{}, ErrOrderNotOpen
	}
	if order.State != market.StateCancelled {
		if order.State != market.StateOpen && order.State != market.StatePartiallyFilled {
			return market.Order{}, ErrOrderNotOpen
		}
		order.State = market.StateCancelled
		s.orders[orderID] = order
	}
	s.cancellations[cancellationID] = cancellationRecord{
		orderID: orderID,
		result:  order,
	}
	return order, nil
}

func (s *Service) Get(ctx context.Context, orderID string) (market.Order, error) {
	if err := ctx.Err(); err != nil {
		return market.Order{}, err
	}
	if strings.TrimSpace(orderID) == "" {
		return market.Order{}, ErrInvalidOrder
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	order, ok := s.orders[orderID]
	if !ok {
		return market.Order{}, ErrOrderNotFound
	}
	return order, nil
}

func (s *Service) OpenOrders(ctx context.Context, intervalStart time.Time) ([]market.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !market.IsValidIntervalStart(intervalStart) {
		return nil, ErrInvalidOrder
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]market.Order, 0)
	for _, order := range s.orders {
		if order.IntervalStart.Equal(intervalStart) &&
			order.RemainingEnergyWh > 0 &&
			(order.State == market.StateOpen || order.State == market.StatePartiallyFilled) {
			result = append(result, order)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (s *Service) GetAuction(ctx context.Context, auctionID string) (market.AuctionResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return market.AuctionResult{}, false, err
	}
	if strings.TrimSpace(auctionID) == "" {
		return market.AuctionResult{}, false, ErrInvalidOrder
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.auctions[auctionID]
	return cloneAuctionResult(result), ok, nil
}

func (s *Service) CommitAuction(ctx context.Context, result market.AuctionResult) (market.AuctionResult, error) {
	if err := ctx.Err(); err != nil {
		return market.AuctionResult{}, err
	}
	if strings.TrimSpace(result.AuctionID) == "" || !market.IsValidIntervalStart(result.IntervalStart) {
		return market.AuctionResult{}, ErrInvalidOrder
	}
	if err := validateAuctionTotals(result); err != nil {
		return market.AuctionResult{}, err
	}

	result.IntervalStart = result.IntervalStart.UTC()
	result = cloneAuctionResult(result)
	s.mu.Lock()
	defer s.mu.Unlock()

	if previous, ok := s.auctions[result.AuctionID]; ok {
		if !sameAuctionResult(previous, result) {
			return market.AuctionResult{}, ErrIdempotencyConflict
		}
		return cloneAuctionResult(previous), nil
	}
	intervalKey := result.IntervalStart.Unix()
	if existingAuctionID, ok := s.auctionByInterval[intervalKey]; ok {
		if existingAuctionID == result.AuctionID {
			return market.AuctionResult{}, ErrIdempotencyConflict
		}
		return market.AuctionResult{}, ErrAuctionAlreadyRun
	}

	filledByOrder := make(map[string]uint64)
	for _, trade := range result.Trades {
		buy, buyOK := s.orders[trade.BuyOrderID]
		sell, sellOK := s.orders[trade.SellOrderID]
		if !buyOK || !sellOK {
			return market.AuctionResult{}, ErrOrderNotFound
		}
		if buy.Side != market.SideBuy ||
			sell.Side != market.SideSell ||
			buy.State == market.StateCancelled ||
			sell.State == market.StateCancelled ||
			!buy.IntervalStart.Equal(result.IntervalStart) ||
			!sell.IntervalStart.Equal(result.IntervalStart) ||
			buy.HouseholdID != trade.BuyerHouseholdID ||
			sell.HouseholdID != trade.SellerHouseholdID ||
			buy.LimitPriceMicrocreditsPerKWh < result.ClosingPriceMicrocreditsPerKWh ||
			sell.LimitPriceMicrocreditsPerKWh > result.ClosingPriceMicrocreditsPerKWh {
			return market.AuctionResult{}, ErrInvalidOrder
		}
		if !addEnergy(filledByOrder, buy.ID, trade.EnergyWh) ||
			!addEnergy(filledByOrder, sell.ID, trade.EnergyWh) {
			return market.AuctionResult{}, ErrInvalidOrder
		}
	}
	for orderID, filledEnergyWh := range filledByOrder {
		order := s.orders[orderID]
		if (order.State != market.StateOpen && order.State != market.StatePartiallyFilled) ||
			filledEnergyWh > order.RemainingEnergyWh {
			return market.AuctionResult{}, ErrOrderNotOpen
		}
	}

	for orderID, filledEnergyWh := range filledByOrder {
		order := s.orders[orderID]
		order.RemainingEnergyWh -= filledEnergyWh
		if order.RemainingEnergyWh == 0 {
			order.State = market.StateFilled
		} else {
			order.State = market.StatePartiallyFilled
		}
		s.orders[orderID] = order
	}
	for orderID, order := range s.orders {
		if order.IntervalStart.Equal(result.IntervalStart) &&
			order.RemainingEnergyWh > 0 &&
			(order.State == market.StateOpen || order.State == market.StatePartiallyFilled) {
			order.State = market.StateExpired
			s.orders[orderID] = order
		}
	}
	s.auctions[result.AuctionID] = result
	s.auctionByInterval[intervalKey] = result.AuctionID
	return cloneAuctionResult(result), nil
}

func validateRequest(request market.PlaceOrderRequest) error {
	if strings.TrimSpace(request.ID) == "" ||
		strings.TrimSpace(request.HouseholdID) == "" ||
		(request.Side != market.SideBuy && request.Side != market.SideSell) ||
		!market.IsValidIntervalStart(request.IntervalStart) ||
		request.EnergyWh == 0 {
		return ErrInvalidOrder
	}
	return nil
}

func validateAuctionTotals(result market.AuctionResult) error {
	var total uint64
	seenTradeIDs := make(map[string]struct{}, len(result.Trades))
	for _, trade := range result.Trades {
		if strings.TrimSpace(trade.ID) == "" ||
			strings.TrimSpace(trade.BuyOrderID) == "" ||
			strings.TrimSpace(trade.SellOrderID) == "" ||
			trade.BuyOrderID == trade.SellOrderID ||
			strings.TrimSpace(trade.BuyerHouseholdID) == "" ||
			strings.TrimSpace(trade.SellerHouseholdID) == "" ||
			trade.EnergyWh == 0 {
			return ErrInvalidOrder
		}
		if _, exists := seenTradeIDs[trade.ID]; exists {
			return ErrInvalidOrder
		}
		seenTradeIDs[trade.ID] = struct{}{}
		if ^uint64(0)-total < trade.EnergyWh {
			return ErrInvalidOrder
		}
		total += trade.EnergyWh
	}
	if total != result.ClearedEnergyWh || (len(result.Trades) == 0 && result.ClosingPriceMicrocreditsPerKWh != 0) {
		return ErrInvalidOrder
	}
	return nil
}

func addEnergy(totals map[string]uint64, orderID string, energyWh uint64) bool {
	if ^uint64(0)-totals[orderID] < energyWh {
		return false
	}
	totals[orderID] += energyWh
	return true
}

func sameRequest(order market.Order, request market.PlaceOrderRequest) bool {
	return order.ID == request.ID &&
		order.HouseholdID == request.HouseholdID &&
		order.Side == request.Side &&
		order.IntervalStart.Equal(request.IntervalStart) &&
		order.EnergyWh == request.EnergyWh &&
		order.LimitPriceMicrocreditsPerKWh == request.LimitPriceMicrocreditsPerKWh
}

func sameAuctionResult(left, right market.AuctionResult) bool {
	if left.AuctionID != right.AuctionID ||
		!left.IntervalStart.Equal(right.IntervalStart) ||
		left.ClearedEnergyWh != right.ClearedEnergyWh ||
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

func cloneAuctionResult(result market.AuctionResult) market.AuctionResult {
	result.Trades = append([]market.Trade(nil), result.Trades...)
	return result
}
