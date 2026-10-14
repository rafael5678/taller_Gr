package market

import "time"

type ClearingBatch struct {
	ID                              string
	IntervalStart                   time.Time
	ClosingPriceMicrocreditsPerKWh  uint64
	Trades                          []Trade
}

type ValidatedTrade struct {
	TradeID          string
	ProposedEnergyWh uint64
	AcceptedEnergyWh uint64
}

type ValidatedBatch struct {
	ID                              string
	IntervalStart                   time.Time
	TransformerLimitWh              uint64
	ProposedEnergyWh                uint64
	AcceptedEnergyWh                uint64
	RecortadoPorRed                 bool
	ClosingPriceMicrocreditsPerKWh  uint64
	Trades                          []ValidatedTrade
}
