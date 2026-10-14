package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/volt-barrio/volt-barrio/pkg/market"
)

const commandQueueCapacity = 16

var (
	ErrInvalidDevice             = errors.New("invalid device")
	ErrInvalidCommand            = errors.New("invalid device command")
	ErrCommandExpired            = errors.New("device command deadline expired")
	ErrCommandNotFound           = errors.New("device command not found")
	ErrCommandAlreadyAcknowledged = errors.New("device command already acknowledged")
	ErrIdempotencyConflict       = errors.New("idempotency key reused with different request")
	ErrStreamBackpressure        = errors.New("device command stream is full")
	ErrStreamClosed              = errors.New("device control stream is closed")
)

type CommandStatus uint8

const (
	CommandStatusUnspecified CommandStatus = iota
	CommandStatusApplied
	CommandStatusRejected
	CommandStatusExpired
)

type Command struct {
	ID                string
	DeviceID          string
	IntervalStart     time.Time
	ExecutionDeadline time.Time
	SetpointWatts     int64
}

type Acknowledgement struct {
	ID                   string
	CommandID            string
	DeviceID             string
	Status               CommandStatus
	AppliedSetpointWatts int64
}

type Service struct {
	now func() time.Time
}

type pendingCommand struct {
	command   Command
	ack       Acknowledgement
	done      chan struct{}
	completed bool
	timedOut  bool
}

type Stream struct {
	ctx              context.Context
	cancel           context.CancelFunc
	deviceID         string
	now              func() time.Time
	commands         chan Command
	mu               sync.Mutex
	pending          map[string]*pendingCommand
	acknowledgements map[string]Acknowledgement
}

func NewService() *Service {
	return &Service{now: time.Now}
}

func newService(now func() time.Time) *Service {
	return &Service{now: now}
}

func (s *Service) OpenStream(ctx context.Context, deviceID string) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deviceID) == "" {
		return nil, ErrInvalidDevice
	}
	streamCtx, cancel := context.WithCancel(ctx)
	return &Stream{
		ctx:              streamCtx,
		cancel:           cancel,
		deviceID:         deviceID,
		now:              s.now,
		commands:         make(chan Command, commandQueueCapacity),
		pending:          make(map[string]*pendingCommand),
		acknowledgements: make(map[string]Acknowledgement),
	}, nil
}

func (s *Stream) SendCommand(ctx context.Context, command Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(command.ID) == "" ||
		command.DeviceID != s.deviceID ||
		!market.IsValidIntervalStart(command.IntervalStart) ||
		!command.ExecutionDeadline.After(command.IntervalStart) ||
		command.ExecutionDeadline.After(command.IntervalStart.Add(market.IntervalDuration)) {
		return ErrInvalidCommand
	}
	if !s.now().Before(command.ExecutionDeadline) {
		return ErrCommandExpired
	}
	command.IntervalStart = command.IntervalStart.UTC()
	command.ExecutionDeadline = command.ExecutionDeadline.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return ErrStreamClosed
	}
	if previous, ok := s.pending[command.ID]; ok {
		if !sameCommand(previous.command, command) {
			return ErrIdempotencyConflict
		}
		return nil
	}
	pending := &pendingCommand{
		command: command,
		done:    make(chan struct{}),
	}
	select {
	case s.commands <- command:
		s.pending[command.ID] = pending
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return ErrStreamClosed
	default:
		return ErrStreamBackpressure
	}
}

func (s *Stream) ReceiveCommand(ctx context.Context) (Command, error) {
	if err := ctx.Err(); err != nil {
		return Command{}, err
	}
	if s.ctx.Err() != nil {
		return Command{}, ErrStreamClosed
	}
	select {
	case command := <-s.commands:
		if !s.now().Before(command.ExecutionDeadline) {
			s.expire(command.ID)
			return Command{}, ErrCommandExpired
		}
		return command, nil
	case <-ctx.Done():
		return Command{}, ctx.Err()
	case <-s.ctx.Done():
		return Command{}, ErrStreamClosed
	}
}

func (s *Stream) Acknowledge(ctx context.Context, acknowledgement Acknowledgement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return ErrStreamClosed
	}
	if strings.TrimSpace(acknowledgement.ID) == "" ||
		strings.TrimSpace(acknowledgement.CommandID) == "" ||
		acknowledgement.DeviceID != s.deviceID ||
		(acknowledgement.Status != CommandStatusApplied &&
			acknowledgement.Status != CommandStatusRejected &&
			acknowledgement.Status != CommandStatusExpired) {
		return ErrInvalidCommand
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return ErrStreamClosed
	}
	if previous, ok := s.acknowledgements[acknowledgement.ID]; ok {
		if previous != acknowledgement {
			return ErrIdempotencyConflict
		}
		return nil
	}
	pending, ok := s.pending[acknowledgement.CommandID]
	if !ok {
		return ErrCommandNotFound
	}
	if pending.completed {
		return ErrCommandAlreadyAcknowledged
	}
	if !s.now().Before(pending.command.ExecutionDeadline) {
		s.completeExpired(pending)
		return ErrCommandExpired
	}

	pending.ack = acknowledgement
	pending.completed = true
	s.acknowledgements[acknowledgement.ID] = acknowledgement
	close(pending.done)
	return nil
}

func (s *Stream) WaitForAcknowledgement(ctx context.Context, commandID string) (Acknowledgement, error) {
	if err := ctx.Err(); err != nil {
		return Acknowledgement{}, err
	}

	s.mu.Lock()
	pending, ok := s.pending[commandID]
	if !ok {
		s.mu.Unlock()
		return Acknowledgement{}, ErrCommandNotFound
	}
	if pending.completed {
		acknowledgement, timedOut := pending.ack, pending.timedOut
		s.mu.Unlock()
		if timedOut {
			return acknowledgement, ErrCommandExpired
		}
		return acknowledgement, nil
	}
	delay := pending.command.ExecutionDeadline.Sub(s.now())
	s.mu.Unlock()
	if delay <= 0 {
		acknowledgement, _ := s.expire(commandID)
		return acknowledgement, ErrCommandExpired
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-pending.done:
		s.mu.Lock()
		acknowledgement, timedOut := pending.ack, pending.timedOut
		s.mu.Unlock()
		if timedOut {
			return acknowledgement, ErrCommandExpired
		}
		return acknowledgement, nil
	case <-timer.C:
		acknowledgement, timedOut := s.expire(commandID)
		if timedOut {
			return acknowledgement, ErrCommandExpired
		}
		return acknowledgement, nil
	case <-ctx.Done():
		return Acknowledgement{}, ctx.Err()
	case <-s.ctx.Done():
		return Acknowledgement{}, ErrStreamClosed
	}
}

func (s *Stream) Close() {
	s.cancel()
}

func (s *Stream) expire(commandID string) (Acknowledgement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pending[commandID]
	if !ok {
		return Acknowledgement{}, false
	}
	if pending.completed {
		return pending.ack, pending.timedOut
	}
	s.completeExpired(pending)
	return pending.ack, true
}

func (s *Stream) completeExpired(pending *pendingCommand) {
	pending.ack = Acknowledgement{
		CommandID: pending.command.ID,
		DeviceID:  pending.command.DeviceID,
		Status:    CommandStatusExpired,
	}
	pending.completed = true
	pending.timedOut = true
	close(pending.done)
}

func sameCommand(left, right Command) bool {
	return left.ID == right.ID &&
		left.DeviceID == right.DeviceID &&
		left.IntervalStart.Equal(right.IntervalStart) &&
		left.ExecutionDeadline.Equal(right.ExecutionDeadline) &&
		left.SetpointWatts == right.SetpointWatts
}
