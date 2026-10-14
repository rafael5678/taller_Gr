package market

import "time"

const IntervalDuration = 5 * time.Minute

type Side uint8

const (
	SideUnspecified Side = iota
	SideBuy
	SideSell
)

type State uint8

const (
	StateUnspecified State = iota
	StateOpen
	StatePartiallyFilled
	StateFilled
	StateCancelled
	StateExpired
)

type PlaceOrderRequest struct {
	ID                             string
	HouseholdID                    string
	Side                           Side
	IntervalStart                  time.Time
	EnergyWh                       uint64
	LimitPriceMicrocreditsPerKWh   uint64
}

type Order struct {
	ID                           string
	HouseholdID                  string
	Side                         Side
	IntervalStart                time.Time
	EnergyWh                     uint64
	RemainingEnergyWh            uint64
	LimitPriceMicrocreditsPerKWh uint64
	CreatedAt                    time.Time
	State                        State
}

type Trade struct {
	ID                 string
	BuyOrderID         string
	SellOrderID        string
	BuyerHouseholdID   string
	SellerHouseholdID  string
	EnergyWh           uint64
}

type AuctionResult struct {
	AuctionID                         string
	IntervalStart                     time.Time
	ClearedEnergyWh                   uint64
	ClosingPriceMicrocreditsPerKWh    uint64
	Trades                            []Trade
}

func IsValidIntervalStart(value time.Time) bool {
	return !value.IsZero() &&
		value.Nanosecond() == 0 &&
		value.Unix()%int64(IntervalDuration/time.Second) == 0
}
