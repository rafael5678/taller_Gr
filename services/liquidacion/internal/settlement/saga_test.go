package settlement

import (
	"context"
	"errors"
	"testing"
)

func TestSettlementSagaAndCompensationAreIdempotent(t *testing.T) {
	store := NewMemoryStore()
	matcher := &fakeMatcher{}
	payment := &fakePayment{}
	service, err := NewService(store, matcher, payment)
	if err != nil {
		t.Fatal(err)
	}

	settlement, err := service.SettleInterval(context.Background(), testSettlement())
	if err != nil {
		t.Fatal(err)
	}
	if settlement.State != StatePosted || matcher.commits != 1 || payment.captures != 1 {
		t.Fatalf("settlement saga did not complete: %+v matcher=%+v payment=%+v", settlement, matcher, payment)
	}
	if _, err := service.SettleInterval(context.Background(), testSettlement()); err != nil {
		t.Fatal(err)
	}
	if matcher.commits != 1 || payment.captures != 1 {
		t.Fatalf("settlement retry repeated side effects: matcher=%+v payment=%+v", matcher, payment)
	}

	compensation := Compensation{
		ID:           "comp-1",
		SettlementID: settlement.ID,
		Reason:       "manual correction",
	}
	if _, err := service.CompensateInterval(context.Background(), compensation); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompensateInterval(context.Background(), compensation); err != nil {
		t.Fatal(err)
	}
	if payment.refunds != 1 || matcher.compensations != 1 {
		t.Fatalf("compensation retry repeated side effects: matcher=%+v payment=%+v", matcher, payment)
	}
	entries, err := store.LedgerEntries(context.Background(), settlement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sumPostings(entries) != 0 {
		t.Fatalf("ledger no longer sums to zero: %+v", entries)
	}
}

func TestPaymentFailureCompensatesMatchingAndDoesNotPostLedger(t *testing.T) {
	store := NewMemoryStore()
	matcher := &fakeMatcher{}
	payment := &fakePayment{captureErr: errors.New("payment unavailable")}
	service, err := NewService(store, matcher, payment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SettleInterval(context.Background(), testSettlement()); err == nil {
		t.Fatal("expected payment failure")
	}
	if matcher.commits != 1 || matcher.compensations != 1 || payment.refunds != 1 {
		t.Fatalf("failed saga was not compensated: matcher=%+v payment=%+v", matcher, payment)
	}
	if _, err := store.GetSettlement(context.Background(), "settlement-1"); !errors.Is(err, ErrSettlementNotFound) {
		t.Fatalf("failed saga unexpectedly posted ledger: %v", err)
	}
}

func TestSettlementCommitWithLostAcknowledgementIsNotCompensated(t *testing.T) {
	store := &commitAcknowledgementLostStore{Store: NewMemoryStore()}
	matcher := &fakeMatcher{}
	payment := &fakePayment{}
	service, err := NewService(store, matcher, payment)
	if err != nil {
		t.Fatal(err)
	}

	settled, err := service.SettleInterval(context.Background(), testSettlement())
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != StatePosted || matcher.compensations != 0 || payment.refunds != 0 {
		t.Fatalf("committed settlement was incorrectly compensated: result=%+v matcher=%+v payment=%+v", settled, matcher, payment)
	}
}

type fakeMatcher struct {
	commits       int
	compensations int
}

func (f *fakeMatcher) CommitSettlement(context.Context, string, string) error {
	f.commits++
	return nil
}

func (f *fakeMatcher) CompensateSettlement(context.Context, string, string) error {
	f.compensations++
	return nil
}

type fakePayment struct {
	captures   int
	refunds    int
	captureErr error
}

func (f *fakePayment) Capture(context.Context, string, []Posting) error {
	f.captures++
	return f.captureErr
}

func (f *fakePayment) Refund(context.Context, string, string) error {
	f.refunds++
	return nil
}

type commitAcknowledgementLostStore struct {
	Store
}

func (s *commitAcknowledgementLostStore) PostSettlement(ctx context.Context, settlement Settlement) (Settlement, error) {
	_, err := s.Store.PostSettlement(ctx, settlement)
	if err != nil {
		return Settlement{}, err
	}
	return Settlement{}, errors.New("commit acknowledgement lost")
}
