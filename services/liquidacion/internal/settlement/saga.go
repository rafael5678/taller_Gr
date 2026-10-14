package settlement

import (
	"context"
	"errors"
	"strings"
)

type MatchingPort interface {
	CommitSettlement(context.Context, string, string) error
	CompensateSettlement(context.Context, string, string) error
}

type PaymentPort interface {
	Capture(context.Context, string, []Posting) error
	Refund(context.Context, string, string) error
}

type Service struct {
	store   Store
	matcher MatchingPort
	payment PaymentPort
}

func NewService(store Store, matcher MatchingPort, payment PaymentPort) (*Service, error) {
	if store == nil || matcher == nil || payment == nil {
		return nil, ErrInvalidSettlement
	}
	return &Service{store: store, matcher: matcher, payment: payment}, nil
}

func (s *Service) SettleInterval(ctx context.Context, settlement Settlement) (Settlement, error) {
	if err := ctx.Err(); err != nil {
		return Settlement{}, err
	}
	settlement, err := normalizeSettlement(settlement)
	if err != nil {
		return Settlement{}, err
	}
	if previous, err := s.store.GetSettlement(ctx, settlement.ID); err == nil {
		if !sameSettlement(previous, settlement) {
			return Settlement{}, ErrIdempotencyConflict
		}
		return previous, nil
	} else if !errors.Is(err, ErrSettlementNotFound) {
		return Settlement{}, err
	}

	if err := s.matcher.CommitSettlement(ctx, settlement.ID, settlement.AuctionID); err != nil {
		rollbackErr := s.matcher.CompensateSettlement(ctx, rollbackID(settlement.ID), settlement.ID)
		return Settlement{}, errors.Join(err, rollbackErr)
	}
	if err := s.payment.Capture(ctx, settlement.ID, settlement.Postings); err != nil {
		compensationID := rollbackID(settlement.ID)
		paymentErr := s.payment.Refund(ctx, compensationID, settlement.ID)
		rollbackErr := s.matcher.CompensateSettlement(ctx, compensationID, settlement.ID)
		return Settlement{}, errors.Join(err, paymentErr, rollbackErr)
	}
	posted, err := s.store.PostSettlement(ctx, settlement)
	if err != nil {
		previous, lookupErr := s.store.GetSettlement(ctx, settlement.ID)
		if lookupErr == nil {
			if sameSettlement(previous, settlement) {
				return previous, nil
			}
			return Settlement{}, errors.Join(err, ErrIdempotencyConflict)
		}
		if !errors.Is(lookupErr, ErrSettlementNotFound) {
			return Settlement{}, errors.Join(err, lookupErr)
		}
		compensationID := rollbackID(settlement.ID)
		paymentErr := s.payment.Refund(ctx, compensationID, settlement.ID)
		matchingErr := s.matcher.CompensateSettlement(ctx, compensationID, settlement.ID)
		return Settlement{}, errors.Join(err, paymentErr, matchingErr)
	}
	return posted, nil
}

func (s *Service) CompensateInterval(ctx context.Context, compensation Compensation) (Compensation, error) {
	if err := ctx.Err(); err != nil {
		return Compensation{}, err
	}
	if strings.TrimSpace(compensation.ID) == "" ||
		strings.TrimSpace(compensation.SettlementID) == "" ||
		strings.TrimSpace(compensation.Reason) == "" {
		return Compensation{}, ErrInvalidSettlement
	}
	if previous, ok, err := s.store.GetCompensation(ctx, compensation.SettlementID); err != nil {
		return Compensation{}, err
	} else if ok {
		if previous.ID != compensation.ID || previous.Reason != compensation.Reason {
			return Compensation{}, ErrAlreadyCompensated
		}
		return previous, nil
	}
	if _, err := s.store.GetSettlement(ctx, compensation.SettlementID); err != nil {
		return Compensation{}, err
	}

	if err := s.payment.Refund(ctx, compensation.ID, compensation.SettlementID); err != nil {
		return Compensation{}, err
	}
	if err := s.matcher.CompensateSettlement(ctx, compensation.ID, compensation.SettlementID); err != nil {
		return Compensation{}, err
	}
	return s.store.Compensate(ctx, compensation)
}

func rollbackID(settlementID string) string {
	return "rollback:" + settlementID
}
