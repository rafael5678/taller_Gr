package measurement

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSignedMeasurementIngestIsIdempotent(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service, err := NewService(store, map[string]SigningKey{
		"meter-key": {HouseholdID: "house-1", PublicKey: publicKey},
	}, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reading := &voltbarrio.MeterReading{
		ReadingId: "reading-1", HouseholdId: "house-1", MeterId: "meter-1",
		IntervalStart: timestamppb.New(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		ProducedEnergyWh: 15, ConsumedEnergyWh: 4,
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	signed := &voltbarrio.SignedMeterReading{
		Reading: reading, KeyId: "meter-key", Signature: ed25519.Sign(privateKey, canonical),
	}
	duplicate, err := service.Ingest(context.Background(), signed)
	if err != nil || duplicate {
		t.Fatalf("first ingestion = duplicate %v, error %v", duplicate, err)
	}
	duplicate, err = service.Ingest(context.Background(), signed)
	if err != nil || !duplicate {
		t.Fatalf("retry = duplicate %v, error %v", duplicate, err)
	}
}

func TestInvalidSignatureIsRecordedAndRejected(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service, err := NewService(store, map[string]SigningKey{
		"meter-key": {HouseholdID: "house-1", PublicKey: publicKey},
	}, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Ingest(context.Background(), &voltbarrio.SignedMeterReading{
		KeyId: "meter-key",
		Reading: &voltbarrio.MeterReading{
			ReadingId: "reading-1", HouseholdId: "house-1", MeterId: "meter-1",
			IntervalStart: timestamppb.New(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		},
		Signature: make([]byte, ed25519.SignatureSize),
	})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected invalid signature, got %v", err)
	}
	if len(store.rejected) != 1 || store.rejected[0].ReadingID != "reading-1" {
		t.Fatalf("invalid attempt was not recorded: %+v", store.rejected)
	}
}

func TestRateLimitIsPerHousehold(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service, err := NewService(store, map[string]SigningKey{
		"key-1": {HouseholdID: "house-1", PublicKey: publicKey},
	}, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	if err := service.takeToken("house-1"); err != nil {
		t.Fatal(err)
	}
	if err := service.takeToken("house-1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected household rate limit, got %v", err)
	}
	if err := service.takeToken("house-2"); err != nil {
		t.Fatalf("another household should have an independent limit: %v", err)
	}
}
