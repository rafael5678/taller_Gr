package settlement

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLedgerRejectsUnbalancedSettlement(t *testing.T) {
	store := NewMemoryStore()
	settlement := testSettlement()
	settlement.Postings[1].SignedAmountMicrocredits++
	if _, err := store.PostSettlement(context.Background(), settlement); !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("expected unbalanced settlement rejection, got %v", err)
	}
}

func TestLedgerEntriesAlwaysSumToZeroAfterCompensation(t *testing.T) {
	store := NewMemoryStore()
	posted, err := store.PostSettlement(context.Background(), testSettlement())
	if err != nil {
		t.Fatal(err)
	}
	if total := sumPostings(posted.Postings); total != 0 {
		t.Fatalf("posted ledger sums to %d", total)
	}
	compensation, err := store.Compensate(context.Background(), Compensation{
		ID:           "comp-1",
		SettlementID: posted.ID,
		Reason:       "test reversal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if total := sumPostings(compensation.ReversalPostings); total != 0 {
		t.Fatalf("reversal entries sum to %d", total)
	}
	entries, err := store.LedgerEntries(context.Background(), posted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if total := sumPostings(entries); total != 0 {
		t.Fatalf("complete ledger sums to %d after compensation", total)
	}
	state, err := store.GetSettlement(context.Background(), posted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != StateCompensated {
		t.Fatalf("expected compensated state, got %v", state.State)
	}
}

func TestSettlementAndCompensationAreIdempotent(t *testing.T) {
	store := NewMemoryStore()
	input := testSettlement()
	first, err := store.PostSettlement(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.PostSettlement(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != retry.ID || len(first.Postings) != len(retry.Postings) {
		t.Fatalf("settlement retry changed result: %+v / %+v", first, retry)
	}
	compensation := Compensation{ID: "comp-1", SettlementID: input.ID, Reason: "test reversal"}
	firstCompensation, err := store.Compensate(context.Background(), compensation)
	if err != nil {
		t.Fatal(err)
	}
	retryCompensation, err := store.Compensate(context.Background(), compensation)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCompensation.ReversalPostings) != len(retryCompensation.ReversalPostings) {
		t.Fatalf("compensation retry changed result: %+v / %+v", firstCompensation, retryCompensation)
	}
}

func TestSettlementIDCannotBeReusedWithDifferentPostings(t *testing.T) {
	store := NewMemoryStore()
	input := testSettlement()
	if _, err := store.PostSettlement(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Postings[0].SignedAmountMicrocredits++
	input.Postings[1].SignedAmountMicrocredits--
	if _, err := store.PostSettlement(context.Background(), input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestPostingSumUsesUnboundedIntermediate(t *testing.T) {
	if isBalanced([]Posting{
		{ID: "a", AccountID: "one", SignedAmountMicrocredits: int64(^uint64(0) >> 1)},
		{ID: "b", AccountID: "two", SignedAmountMicrocredits: 1},
		{ID: "c", AccountID: "three", SignedAmountMicrocredits: -int64(^uint64(0) >> 1)},
	}) {
		t.Fatal("unbalanced postings unexpectedly passed validation")
	}
}

func testSettlement() Settlement {
	return Settlement{
		ID:            "settlement-1",
		AuctionID:     "auction-1",
		IntervalStart: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
		Postings: []Posting{
			{ID: "entry-debit", AccountID: "household-buyer", SignedAmountMicrocredits: 1250},
			{ID: "entry-credit", AccountID: "household-seller", SignedAmountMicrocredits: -1250},
		},
	}
}

func sumPostings(postings []Posting) int64 {
	var total int64
	for _, posting := range postings {
		total += posting.SignedAmountMicrocredits
	}
	return total
}
