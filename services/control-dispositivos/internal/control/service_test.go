package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBidirectionalCommandAcknowledgement(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC)
	stream, err := newService(func() time.Time { return now }).OpenStream(context.Background(), "battery-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	command := testCommand("command-1", "battery-1", now.Add(time.Minute))
	if err := stream.SendCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	received, err := stream.ReceiveCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if received.ID != command.ID || received.SetpointWatts != command.SetpointWatts {
		t.Fatalf("device received different command: %+v", received)
	}
	acknowledgement := Acknowledgement{
		ID:                   "ack-1",
		CommandID:            command.ID,
		DeviceID:             command.DeviceID,
		Status:               CommandStatusApplied,
		AppliedSetpointWatts: command.SetpointWatts,
	}
	if err := stream.Acknowledge(context.Background(), acknowledgement); err != nil {
		t.Fatal(err)
	}
	result, err := stream.WaitForAcknowledgement(context.Background(), command.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result != acknowledgement {
		t.Fatalf("unexpected acknowledgement: %+v", result)
	}
}

func TestCommandRetryIsIdempotentAndChangedPayloadConflicts(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC)
	stream, err := newService(func() time.Time { return now }).OpenStream(context.Background(), "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	command := testCommand("command-1", "ev-1", now.Add(time.Minute))
	if err := stream.SendCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := stream.SendCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	received, err := stream.ReceiveCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if received.ID != command.ID {
		t.Fatalf("unexpected command: %+v", received)
	}
	command.SetpointWatts++
	if err := stream.SendCommand(context.Background(), command); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected command idempotency conflict, got %v", err)
	}
}

func TestAcknowledgementIsIdempotent(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC)
	stream, err := newService(func() time.Time { return now }).OpenStream(context.Background(), "battery-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	command := testCommand("command-1", "battery-1", now.Add(time.Minute))
	if err := stream.SendCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.ReceiveCommand(context.Background()); err != nil {
		t.Fatal(err)
	}
	acknowledgement := Acknowledgement{
		ID:        "ack-1",
		CommandID: command.ID,
		DeviceID:  command.DeviceID,
		Status:    CommandStatusApplied,
	}
	if err := stream.Acknowledge(context.Background(), acknowledgement); err != nil {
		t.Fatal(err)
	}
	if err := stream.Acknowledge(context.Background(), acknowledgement); err != nil {
		t.Fatalf("exact acknowledgement retry failed: %v", err)
	}
	acknowledgement.Status = CommandStatusRejected
	if err := stream.Acknowledge(context.Background(), acknowledgement); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected acknowledgement idempotency conflict, got %v", err)
	}
}

func TestWaitForAcknowledgementExpiresAtCommandDeadline(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC)
	stream, err := newService(func() time.Time { return now }).OpenStream(context.Background(), "battery-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	command := testCommand("command-timeout", "battery-1", now.Add(20*time.Millisecond))
	if err := stream.SendCommand(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	result, err := stream.WaitForAcknowledgement(context.Background(), command.ID)
	if !errors.Is(err, ErrCommandExpired) || result.Status != CommandStatusExpired {
		t.Fatalf("expected expired status and deadline error, got result=%+v err=%v", result, err)
	}
	if err := stream.Acknowledge(context.Background(), Acknowledgement{
		ID: "late-ack", CommandID: command.ID, DeviceID: command.DeviceID, Status: CommandStatusApplied,
	}); !errors.Is(err, ErrCommandAlreadyAcknowledged) {
		t.Fatalf("expected late acknowledgement rejection, got %v", err)
	}
}

func TestCommandCannotTargetAnotherDeviceOrOutliveInterval(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 1, 0, 0, time.UTC)
	stream, err := newService(func() time.Time { return now }).OpenStream(context.Background(), "battery-1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	command := testCommand("command-1", "ev-1", now.Add(time.Minute))
	if err := stream.SendCommand(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("expected wrong-device rejection, got %v", err)
	}
	command = testCommand("command-2", "battery-1", now.Add(6*time.Minute))
	if err := stream.SendCommand(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("expected interval-deadline rejection, got %v", err)
	}
}

func testCommand(id, deviceID string, deadline time.Time) Command {
	return Command{
		ID:                id,
		DeviceID:          deviceID,
		IntervalStart:     time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
		ExecutionDeadline: deadline,
		SetpointWatts:     1500,
	}
}
