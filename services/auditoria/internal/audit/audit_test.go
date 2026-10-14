package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuditChainVerifiesAppendedRecords(t *testing.T) {
	store := NewMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Append(context.Background(), Event{
		ID: "event-1", Type: "order.accepted", ActorID: "household-1", Payload: []byte("order=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Append(context.Background(), Event{
		ID: "event-2", Type: "auction.closed", ActorID: "operator-1", Payload: []byte("auction=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 || string(second.PreviousHash) != string(first.RecordHash) {
		t.Fatalf("records are not chained: first=%+v second=%+v", first, second)
	}
	verification, err := service.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Valid || verification.RecordsChecked != 2 || verification.HashAlgorithm != "SHA-256" {
		t.Fatalf("unexpected chain verification: %+v", verification)
	}
}

func TestTamperingWithRecordBreaksVerification(t *testing.T) {
	store := NewMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Append(context.Background(), Event{
		ID: "event-1", Type: "order.accepted", ActorID: "household-1", Payload: []byte("original"),
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.records[0].Event.Payload[0] ^= 0xff
	store.mu.Unlock()

	verification, err := service.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if verification.Valid || verification.FirstInvalidSequence != 1 {
		t.Fatalf("tampered chain unexpectedly verified: %+v", verification)
	}
}

func TestAuditEventIDIsIdempotentAndContentBound(t *testing.T) {
	store := NewMemoryStore()
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{ID: "event-1", Type: "meter.reading", ActorID: "meter-1", Payload: []byte("signed")}
	first, err := service.Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != retry.Sequence || string(first.RecordHash) != string(retry.RecordHash) {
		t.Fatalf("event retry changed record: first=%+v retry=%+v", first, retry)
	}
	event.Payload = []byte("different")
	if _, err := service.Append(context.Background(), event); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected event idempotency conflict, got %v", err)
	}
}

func TestAuditTimestampUsesPostgresPrecision(t *testing.T) {
	store := NewMemoryStore()
	recordedAt := time.Date(2026, time.January, 1, 12, 0, 0, 123456789, time.FixedZone("test", -5*60*60))
	record, err := store.Append(context.Background(), Event{
		ID: "event-1", Type: "order.accepted", ActorID: "household-1",
	}, recordedAt)
	if err != nil {
		t.Fatal(err)
	}
	if want := recordedAt.UTC().Truncate(time.Microsecond); !record.RecordedAt.Equal(want) {
		t.Fatalf("recorded timestamp = %s, want %s", record.RecordedAt, want)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := service.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Valid {
		t.Fatalf("normalized timestamp broke chain verification: %+v", verification)
	}
}
