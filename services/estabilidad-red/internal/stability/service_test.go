package stability

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

func TestProrationNeverExceedsTransformerLimit(t *testing.T) {
	random := rand.New(rand.NewSource(20261013))
	for caseNumber := 0; caseNumber < 1000; caseNumber++ {
		tradeCount := 1 + random.Intn(20)
		trades := make([]market.Trade, tradeCount)
		var proposed uint64
		for i := range trades {
			energyWh := uint64(1 + random.Intn(1_000_000))
			proposed += energyWh
			trades[i] = testTrade(string(rune('a'+i)), energyWh)
		}
		limit := uint64(random.Int63n(int64(proposed + 1)))
		service := NewService(limit)
		result, err := service.ValidateBatch(context.Background(), testBatch("batch", trades))
		if err != nil {
			t.Fatalf("case %d: unexpected validation error: %v", caseNumber, err)
		}
		if result.AcceptedEnergyWh > limit {
			t.Fatalf("case %d: accepted %d Wh above limit %d Wh", caseNumber, result.AcceptedEnergyWh, limit)
		}
		if result.AcceptedEnergyWh != min(limit, proposed) {
			t.Fatalf("case %d: expected %d Wh accepted, got %d Wh", caseNumber, min(limit, proposed), result.AcceptedEnergyWh)
		}
		var accepted uint64
		for i, trade := range result.Trades {
			if trade.AcceptedEnergyWh > trade.ProposedEnergyWh {
				t.Fatalf("case %d trade %d: accepted energy exceeds proposal", caseNumber, i)
			}
			accepted += trade.AcceptedEnergyWh
		}
		if accepted != result.AcceptedEnergyWh {
			t.Fatalf("case %d: per-trade total %d differs from batch total %d", caseNumber, accepted, result.AcceptedEnergyWh)
		}
	}
}

func TestProrationHandlesMaximumIntegerWithoutOverflow(t *testing.T) {
	maxEnergy := ^uint64(0)
	service := NewService(maxEnergy / 2)
	result, err := service.ValidateBatch(context.Background(), testBatch("batch-max", []market.Trade{
		testTrade("trade-max", maxEnergy),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedEnergyWh != maxEnergy/2 || result.AcceptedEnergyWh > result.TransformerLimitWh {
		t.Fatalf("unexpected maximum-value allocation: %+v", result)
	}
}

func TestProrationTieBreakIsDeterministic(t *testing.T) {
	service := NewService(1)
	batch := testBatch("batch-tie", []market.Trade{
		testTrade("trade-b", 1),
		testTrade("trade-a", 1),
	})
	first, err := service.ValidateBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if first.Trades[0].AcceptedEnergyWh != 0 || first.Trades[1].AcceptedEnergyWh != 1 {
		t.Fatalf("remaining Wh should go to lexicographically first trade: %+v", first.Trades)
	}
	retry, err := service.ValidateBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Trades[0] != first.Trades[0] || retry.Trades[1] != first.Trades[1] {
		t.Fatalf("retry changed proration: first=%+v retry=%+v", first.Trades, retry.Trades)
	}
}

func TestValidateBatchRejectsReusedIDWithDifferentContents(t *testing.T) {
	service := NewService(100)
	batch := testBatch("batch-1", []market.Trade{testTrade("trade-1", 50)})
	if _, err := service.ValidateBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	batch.Trades[0].EnergyWh++
	if _, err := service.ValidateBatch(context.Background(), batch); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestIntervalCannotBeValidatedAsMultipleBatches(t *testing.T) {
	service := NewService(100)
	first := testBatch("batch-1", []market.Trade{testTrade("trade-1", 60)})
	if _, err := service.ValidateBatch(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testBatch("batch-2", []market.Trade{testTrade("trade-2", 60)})
	if _, err := service.ValidateBatch(context.Background(), second); !errors.Is(err, ErrIntervalAlreadyValidated) {
		t.Fatalf("expected interval-conflict error, got %v", err)
	}
}

func TestUncurtailedBatchPreservesEnergy(t *testing.T) {
	trades := []market.Trade{testTrade("trade-1", 20), testTrade("trade-2", 30)}
	result, err := NewService(50).ValidateBatch(context.Background(), testBatch("batch-full", trades))
	if err != nil {
		t.Fatal(err)
	}
	if result.RecortadoPorRed || result.AcceptedEnergyWh != 50 {
		t.Fatalf("batch at its exact limit should not be curtailed: %+v", result)
	}
}

func testBatch(id string, trades []market.Trade) market.ClearingBatch {
	return market.ClearingBatch{
		ID:            id,
		IntervalStart: testInterval(),
		Trades:        trades,
	}
}

func testTrade(id string, energyWh uint64) market.Trade {
	return market.Trade{
		ID:                id,
		BuyerHouseholdID:  "buyer-" + id,
		SellerHouseholdID: "seller-" + id,
		EnergyWh:          energyWh,
	}
}

func testInterval() time.Time {
	return time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
}
