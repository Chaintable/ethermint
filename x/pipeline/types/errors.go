package types

import "cosmossdk.io/errors"

var (
	ErrNoHistoricalInfo = errors.Register(ModuleName, 38, "no historical info found")
)
