package matching

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

func TestClearAuctionWithNoOffers(t *testing.T) {
	result, err := ClearAuction("auction-empty", testInterval(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClearedEnergyWh != 0 || result.ClosingPriceMicrocreditsPerKWh != 0 || len(result.Trades) != 0 {
		t.Fatalf("empty auction should have no trades or price: %+v", result)
	}
}

func TestClearAuctionWithNoCrossingOrders(t *testing.T) {
	orders := []market.Order{
		testOrder("buy-1", market.SideBuy, 90, 100, "buyer"),
		testOrder("sell-1", market.SideSell, 100, 100, "seller"),
	}
	result, err := ClearAuction("auction-no-cross", testInterval(), orders)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClearedEnergyWh != 0 || result.ClosingPriceMicrocreditsPerKWh != 0 || len(result.Trades) != 0 {
		t.Fatalf("non-crossing orders must not clear: %+v", result)
	}
}

func TestClearAuctionUsesOneClosingPriceAndMatchesEnergy(t *testing.T) {
	orders := []market.Order{
		testOrder("buy-1", market.SideBuy, 101, 70, "buyer"),
		testOrder("sell-1", market.SideSell, 100, 40, "seller"),
	}
	result, err := ClearAuction("auction-cross", testInterval(), orders)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClearedEnergyWh != 40 || len(result.Trades) != 1 || result.Trades[0].EnergyWh != 40 {
		t.Fatalf("unexpected matched quantity: %+v", result)
	}
	if result.ClosingPriceMicrocreditsPerKWh != 100 {
		t.Fatalf("expected floor midpoint of 100, got %d", result.ClosingPriceMicrocreditsPerKWh)
	}
}

func TestClearAuctionBreaksPriceTiesByCreationTimeThenOrderID(t *testing.T) {
	orders := []market.Order{
		testOrder("buy-z", market.SideBuy, 100, 1, "buyer-z"),
		testOrder("buy-a", market.SideBuy, 100, 1, "buyer-a"),
		testOrder("sell-z", market.SideSell, 80, 1, "seller-z"),
		testOrder("sell-a", market.SideSell, 80, 1, "seller-a"),
	}
	result, err := ClearAuction("auction-tie", testInterval(), orders)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 2 {
		t.Fatalf("expected two trades, got %d", len(result.Trades))
	}
	if result.Trades[0].BuyOrderID != "buy-a" || result.Trades[0].SellOrderID != "sell-a" ||
		result.Trades[1].BuyOrderID != "buy-z" || result.Trades[1].SellOrderID != "sell-z" {
		t.Fatalf("tie ordering was not deterministic: %+v", result.Trades)
	}
}

func TestClearAuctionRejectsDuplicateOrderIDs(t *testing.T) {
	order := testOrder("duplicate", market.SideBuy, 100, 10, "buyer")
	if _, err := ClearAuction("auction-duplicate", testInterval(), []market.Order{order, order}); !errors.Is(err, ErrInvalidAuction) {
		t.Fatalf("expected invalid-auction error, got %v", err)
	}
}

func TestAuctionServiceReturnsCommittedResultOnRetry(t *testing.T) {
	book := &fakeOrderBook{
		orders: []market.Order{
			testOrder("buy-1", market.SideBuy, 101, 70, "buyer"),
			testOrder("sell-1", market.SideSell, 100, 40, "seller"),
		},
		auctions: make(map[string]market.AuctionResult),
	}
	service := NewService(book)
	first, err := service.RunAuction(context.Background(), "auction-1", testInterval())
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.RunAuction(context.Background(), "auction-1", testInterval())
	if err != nil {
		t.Fatal(err)
	}
	if first.ClearedEnergyWh != retry.ClearedEnergyWh || first.ClosingPriceMicrocreditsPerKWh != retry.ClosingPriceMicrocreditsPerKWh {
		t.Fatalf("retry changed auction result: first=%+v retry=%+v", first, retry)
	}
	if book.commitCount != 1 {
		t.Fatalf("expected one committed auction, got %d", book.commitCount)
	}
}

func testOrder(id string, side market.Side, price, energy uint64, household string) market.Order {
	return market.Order{
		ID:                           id,
		HouseholdID:                  household,
		Side:                         side,
		IntervalStart:                testInterval(),
		EnergyWh:                     energy,
		RemainingEnergyWh:            energy,
		LimitPriceMicrocreditsPerKWh: price,
		CreatedAt:                    time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		State:                        market.StateOpen,
	}
}

func testInterval() time.Time {
	return time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
}

type fakeOrderBook struct {
	orders      []market.Order
	auctions    map[string]market.AuctionResult
	commitCount int
}

func (f *fakeOrderBook) OpenOrders(_ context.Context, _ time.Time) ([]market.Order, error) {
	return append([]market.Order(nil), f.orders...), nil
}

func (f *fakeOrderBook) GetAuction(_ context.Context, auctionID string) (market.AuctionResult, bool, error) {
	result, ok := f.auctions[auctionID]
	return result, ok, nil
}

func (f *fakeOrderBook) CommitAuction(_ context.Context, result market.AuctionResult) (market.AuctionResult, error) {
	f.commitCount++
	f.auctions[result.AuctionID] = result
	return result, nil
}
