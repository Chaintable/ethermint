package types

import (
	"github.com/ethereum/go-ethereum/common/hexutil"

	dtypes "github.com/evmos/ethermint/debank/types"
)

type DebankOutPutJs struct {
	BlockFile      *dtypes.BlockFile `json:"block_file"`
	Header         *dtypes.Header    `json:"header"`
	StateDiff      hexutil.Bytes     `json:"state_diff"`
	ValidationHash int64             `json:"validation_hash"`
}
