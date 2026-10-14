package matching

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

var (
	ErrInvalidAuction    = errors.New("invalid auction")
	ErrAuctionIDConflict = errors.New("auction id reused for a different interval")
	ErrAuctionAlreadyRun = errors.New("auction already run for interval")
)

type OrderBook interface {
	OpenOrders(context.Context, time.Time) ([]market.Order, error)
	GetAuction(context.Context, string) (market.AuctionResult, bool, error)
	CommitAuction(context.Context, market.AuctionResult) (market.AuctionResult, error)
}

type Service struct {
	mu          sync.Mutex
	orderBook   OrderBook
	results     map[string]market.AuctionResult
	byInterval  map[int64]string
}

func NewService(orderBook OrderBook) *Service {
	return &Service{
		orderBook:  orderBook,
		results:    make(map[string]market.AuctionResult),
		byInterval: make(map[int64]string),
	}
}

func (s *Service) RunAuction(ctx context.Context, auctionID string, intervalStart time.Time) (market.AuctionResult, error) {
	if err := ctx.Err(); err != nil {
		return market.AuctionResult{}, err
	}
	if strings.TrimSpace(auctionID) == "" || !market.IsValidIntervalStart(intervalStart) || s.orderBook == nil {
		return market.AuctionResult{}, ErrInvalidAuction
	}
	intervalStart = intervalStart.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.results[auctionID]; ok {
		if !previous.IntervalStart.Equal(intervalStart) {
			return market.AuctionResult{}, ErrAuctionIDConflict
		}
		return cloneResult(previous), nil
	}
	if existingAuctionID, ok := s.byInterval[intervalStart.Unix()]; ok && existingAuctionID != auctionID {
		return market.AuctionResult{}, ErrAuctionAlreadyRun
	}

	previous, found, err := s.orderBook.GetAuction(ctx, auctionID)
	if err != nil {
		return market.AuctionResult{}, err
	}
	if found {
		if !previous.IntervalStart.Equal(intervalStart) {
			return market.AuctionResult{}, ErrAuctionIDConflict
		}
		s.remember(previous)
		return cloneResult(previous), nil
	}

	orders, err := s.orderBook.OpenOrders(ctx, intervalStart)
	if err != nil {
		return market.AuctionResult{}, err
	}
	proposed, err := ClearAuction(auctionID, intervalStart, orders)
	if err != nil {
		return market.AuctionResult{}, err
	}
	committed, err := s.orderBook.CommitAuction(ctx, proposed)
	if err != nil {
		return market.AuctionResult{}, err
	}
	if committed.AuctionID != auctionID || !committed.IntervalStart.Equal(intervalStart) {
		return market.AuctionResult{}, ErrInvalidAuction
	}
	s.remember(committed)
	return cloneResult(committed), nil
}

func ClearAuction(auctionID string, intervalStart time.Time, input []market.Order) (market.AuctionResult, error) {
	if strings.TrimSpace(auctionID) == "" || !market.IsValidIntervalStart(intervalStart) {
		return market.AuctionResult{}, ErrInvalidAuction
	}
	intervalStart = intervalStart.UTC()

	buys := make([]market.Order, 0)
	sells := make([]market.Order, 0)
	seenOrderIDs := make(map[string]struct{}, len(input))
	for _, order := range input {
		if !order.IntervalStart.Equal(intervalStart) ||
			order.State == market.StateCancelled ||
			order.State == market.StateFilled ||
			order.State == market.StateExpired {
			continue
		}
		if strings.TrimSpace(order.ID) == "" ||
			strings.TrimSpace(order.HouseholdID) == "" ||
			order.RemainingEnergyWh == 0 ||
			order.RemainingEnergyWh > order.EnergyWh ||
			(order.State != market.StateOpen && order.State != market.StatePartiallyFilled) {
			return market.AuctionResult{}, ErrInvalidAuction
		}
		if _, exists := seenOrderIDs[order.ID]; exists {
			return market.AuctionResult{}, ErrInvalidAuction
		}
		seenOrderIDs[order.ID] = struct{}{}
		switch order.Side {
		case market.SideBuy:
			buys = append(buys, order)
		case market.SideSell:
			sells = append(sells, order)
		default:
			return market.AuctionResult{}, ErrInvalidAuction
		}
	}

	sort.Slice(buys, func(i, j int) bool {
		if buys[i].LimitPriceMicrocreditsPerKWh != buys[j].LimitPriceMicrocreditsPerKWh {
			return buys[i].LimitPriceMicrocreditsPerKWh > buys[j].LimitPriceMicrocreditsPerKWh
		}
		return earlierOrder(buys[i], buys[j])
	})
	sort.Slice(sells, func(i, j int) bool {
		if sells[i].LimitPriceMicrocreditsPerKWh != sells[j].LimitPriceMicrocreditsPerKWh {
			return sells[i].LimitPriceMicrocreditsPerKWh < sells[j].LimitPriceMicrocreditsPerKWh
		}
		return earlierOrder(sells[i], sells[j])
	})

	result := market.AuctionResult{
		AuctionID:     auctionID,
		IntervalStart: intervalStart,
	}
	if len(buys) == 0 || len(sells) == 0 {
		return result, nil
	}

	buyIndex, sellIndex := 0, 0
	buyRemaining := buys[0].RemainingEnergyWh
	sellRemaining := sells[0].RemainingEnergyWh
	var marginalBid, marginalAsk uint64
	for buyIndex < len(buys) && sellIndex < len(sells) {
		buy := buys[buyIndex]
		sell := sells[sellIndex]
		if buy.LimitPriceMicrocreditsPerKWh < sell.LimitPriceMicrocreditsPerKWh {
			break
		}

		energyWh := buyRemaining
		if sellRemaining < energyWh {
			energyWh = sellRemaining
		}
		if ^uint64(0)-result.ClearedEnergyWh < energyWh {
			return market.AuctionResult{}, ErrInvalidAuction
		}
		result.ClearedEnergyWh += energyWh
		result.Trades = append(result.Trades, market.Trade{
			ID:                tradeID(auctionID, uint64(len(result.Trades)+1)),
			BuyOrderID:        buy.ID,
			SellOrderID:       sell.ID,
			BuyerHouseholdID:  buy.HouseholdID,
			SellerHouseholdID: sell.HouseholdID,
			EnergyWh:          energyWh,
		})
		marginalBid = buy.LimitPriceMicrocreditsPerKWh
		marginalAsk = sell.LimitPriceMicrocreditsPerKWh

		buyRemaining -= energyWh
		sellRemaining -= energyWh
		if buyRemaining == 0 {
			buyIndex++
			if buyIndex < len(buys) {
				buyRemaining = buys[buyIndex].RemainingEnergyWh
			}
		}
		if sellRemaining == 0 {
			sellIndex++
			if sellIndex < len(sells) {
				sellRemaining = sells[sellIndex].RemainingEnergyWh
			}
		}
	}
	if len(result.Trades) > 0 {
		result.ClosingPriceMicrocreditsPerKWh = marginalAsk + (marginalBid-marginalAsk)/2
	}
	return result, nil
}

func earlierOrder(left, right market.Order) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

func tradeID(auctionID string, sequence uint64) string {
	hash := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(auctionID)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(auctionID))
	binary.BigEndian.PutUint64(size[:], sequence)
	_, _ = hash.Write(size[:])
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *Service) remember(result market.AuctionResult) {
	result = cloneResult(result)
	s.results[result.AuctionID] = result
	s.byInterval[result.IntervalStart.Unix()] = result.AuctionID
}

func cloneResult(result market.AuctionResult) market.AuctionResult {
	result.Trades = append([]market.Trade(nil), result.Trades...)
	return result
}
