package types

import (
	"github.com/ethereum/go-ethereum/common/hexutil"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
)

type DebankOutPut struct {
	BlockFile      *dtypes.BlockFile      `json:"block_file"`
	Header         *dtypes.Header         `json:"header"`
	StateDiff      *dtypes.BlockStateDiff `json:"state_diff"`
	ValidationHash int64                  `json:"validation_hash"`
}

type DebankOutPutJs struct {
	BlockFile      *dtypes.BlockFile `json:"block_file"`
	Header         *dtypes.Header    `json:"header"`
	StateDiff      hexutil.Bytes     `json:"state_diff"`
	ValidationHash int64             `json:"validation_hash"`
}

type BGTraceStatus struct {
	Start     uint64  `json:"start"`
	End       uint64  `json:"end"`
	Latest    uint64  `json:"latest"`
	Blocks    uint64  `json:"blocks"`
	StartTime uint64  `json:"start_time"`
	Duration  uint64  `json:"duration"`
	Rate      float64 `json:"rate"`
}
