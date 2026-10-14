package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

func TestSubmitOrderIsIdempotent(t *testing.T) {
	service := NewService()
	request := market.PlaceOrderRequest{
		ID:                           "order-1",
		HouseholdID:                  "home-1",
		Side:                         market.SideBuy,
		IntervalStart:                testInterval(),
		EnergyWh:                     500,
		LimitPriceMicrocreditsPerKWh: 120,
	}

	first, err := service.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first != retry {
		t.Fatalf("retry changed the accepted order: first=%+v retry=%+v", first, retry)
	}
}

func TestSubmitRejectsOrderIDReuseWithDifferentContents(t *testing.T) {
	service := NewService()
	request := market.PlaceOrderRequest{
		ID:            "order-1",
		HouseholdID:   "home-1",
		Side:          market.SideBuy,
		IntervalStart: testInterval(),
		EnergyWh:      500,
	}
	if _, err := service.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.EnergyWh++
	if _, err := service.Submit(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestSubmitRejectsMisalignedInterval(t *testing.T) {
	service := NewService()
	request := market.PlaceOrderRequest{
		ID:            "order-1",
		HouseholdID:   "home-1",
		Side:          market.SideSell,
		IntervalStart: testInterval().Add(time.Second),
		EnergyWh:      1,
	}
	if _, err := service.Submit(context.Background(), request); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected invalid-order error, got %v", err)
	}
}

func TestCancelIsIdempotentAndCancellationKeyCannotBeReused(t *testing.T) {
	service := NewService()
	for _, id := range []string{"order-1", "order-2"} {
		_, err := service.Submit(context.Background(), market.PlaceOrderRequest{
			ID:            id,
			HouseholdID:   "home-1",
			Side:          market.SideBuy,
			IntervalStart: testInterval(),
			EnergyWh:      100,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	first, err := service.Cancel(context.Background(), "cancel-1", "order-1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.Cancel(context.Background(), "cancel-1", "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != retry || retry.State != market.StateCancelled {
		t.Fatalf("cancel retry changed result: first=%+v retry=%+v", first, retry)
	}
	if _, err := service.Cancel(context.Background(), "cancel-1", "order-2"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected cancellation-key conflict, got %v", err)
	}
}

func TestCommitAuctionFillsBothSidesAndIsIdempotent(t *testing.T) {
	service := NewService()
	for _, request := range []market.PlaceOrderRequest{
		{ID: "buy-1", HouseholdID: "buyer", Side: market.SideBuy, IntervalStart: testInterval(), EnergyWh: 100, LimitPriceMicrocreditsPerKWh: 120},
		{ID: "sell-1", HouseholdID: "seller", Side: market.SideSell, IntervalStart: testInterval(), EnergyWh: 100, LimitPriceMicrocreditsPerKWh: 80},
	} {
		if _, err := service.Submit(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	result := market.AuctionResult{
		AuctionID:                      "auction-1",
		IntervalStart:                  testInterval(),
		ClearedEnergyWh:                100,
		ClosingPriceMicrocreditsPerKWh: 100,
		Trades: []market.Trade{{
			ID: "trade-1", BuyOrderID: "buy-1", SellOrderID: "sell-1",
			BuyerHouseholdID: "buyer", SellerHouseholdID: "seller", EnergyWh: 100,
		}},
	}

	first, err := service.CommitAuction(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.CommitAuction(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Trades) != 1 || len(retry.Trades) != 1 {
		t.Fatalf("commit result lost trades: first=%+v retry=%+v", first, retry)
	}
	for _, id := range []string{"buy-1", "sell-1"} {
		order, err := service.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if order.State != market.StateFilled || order.RemainingEnergyWh != 0 {
			t.Fatalf("order %s was not filled: %+v", id, order)
		}
	}
}

func TestUnbalancedAuctionDoesNotMutateOrders(t *testing.T) {
	service := NewService()
	for _, request := range []market.PlaceOrderRequest{
		{ID: "buy-1", HouseholdID: "buyer", Side: market.SideBuy, IntervalStart: testInterval(), EnergyWh: 100, LimitPriceMicrocreditsPerKWh: 120},
		{ID: "sell-1", HouseholdID: "seller", Side: market.SideSell, IntervalStart: testInterval(), EnergyWh: 100, LimitPriceMicrocreditsPerKWh: 80},
	} {
		if _, err := service.Submit(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	result := market.AuctionResult{
		AuctionID:                      "auction-1",
		IntervalStart:                  testInterval(),
		ClearedEnergyWh:                99,
		ClosingPriceMicrocreditsPerKWh: 100,
		Trades: []market.Trade{{
			ID: "trade-1", BuyOrderID: "buy-1", SellOrderID: "sell-1",
			BuyerHouseholdID: "buyer", SellerHouseholdID: "seller", EnergyWh: 100,
		}},
	}

	if _, err := service.CommitAuction(context.Background(), result); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("expected invalid-auction error, got %v", err)
	}
	for _, id := range []string{"buy-1", "sell-1"} {
		order, err := service.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if order.State != market.StateOpen || order.RemainingEnergyWh != 100 {
			t.Fatalf("invalid auction partially changed order %s: %+v", id, order)
		}
	}
}

func TestEmptyAuctionExpiresRemaindersAndClosesInterval(t *testing.T) {
	service := NewService()
	request := market.PlaceOrderRequest{
		ID:            "order-1",
		HouseholdID:   "home-1",
		Side:          market.SideSell,
		IntervalStart: testInterval(),
		EnergyWh:      100,
	}
	if _, err := service.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, err := service.CommitAuction(context.Background(), market.AuctionResult{
		AuctionID:     "auction-empty",
		IntervalStart: testInterval(),
	})
	if err != nil {
		t.Fatal(err)
	}

	order, err := service.Get(context.Background(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.State != market.StateExpired || order.RemainingEnergyWh != 100 {
		t.Fatalf("unmatched order was not expired: %+v", order)
	}
	if _, err := service.Submit(context.Background(), market.PlaceOrderRequest{
		ID:            "late-order",
		HouseholdID:   "home-2",
		Side:          market.SideBuy,
		IntervalStart: testInterval(),
		EnergyWh:      1,
	}); !errors.Is(err, ErrIntervalClosed) {
		t.Fatalf("expected closed-interval error, got %v", err)
	}
}

func testInterval() time.Time {
	return time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
}
