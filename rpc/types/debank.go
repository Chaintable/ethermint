package types

import (
	"encoding/json"

	evmtypes "github.com/evmos/ethermint/x/evm/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type DebankBlockContext struct {
	BlockId   BlockNumberOrHash `json:"block_id"`
	BlockType BlockType         `json:"type"`
}

type BlockType int

const (
	BlockTypeEquals BlockType = iota
	BlockTypeContains
)

func (bt BlockType) String() string {
	switch bt {
	case BlockTypeContains:
		return "Contains"
	case BlockTypeEquals:
		return "Equals"
	default:
		return "Equals"
	}
}

func (bt BlockType) MarshalJSON() ([]byte, error) {
	return json.Marshal(bt.String())
}

func (bt *BlockType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "Contains":
		*bt = BlockTypeContains
	case "Equals":
		*bt = BlockTypeEquals
	default:
		*bt = BlockTypeEquals
	}
	return nil
}

func (c *DebankBlockContext) GetBlockNumberOrHash() BlockNumberOrHash {
	switch c.BlockType {
	case BlockTypeEquals:
		return c.BlockId
	case BlockTypeContains:
		blockNumber := EthLatestBlockNumber
		return BlockNumberOrHash{
			BlockNumber: &blockNumber,
		}
	default:
		blockNumber := EthLatestBlockNumber
		return BlockNumberOrHash{BlockNumber: &blockNumber}
	}
}

type DebankSingleCallResult struct {
	Code      int           `json:"code"`
	Err       string        `json:"err"`
	FromCache bool          `json:"from_cache"`
	Result    hexutil.Bytes `json:"result"`
	GasUsed   int64         `json:"gas_used"`
	TimeCost  float64       `json:"time_cost"`
}

type DebankMultiCallStats struct {
	BlockNum     uint64      `json:"block_num"`
	BlockHash    common.Hash `json:"block_hash"`
	BlockTime    int64       `json:"block_time"`
	Success      bool        `json:"success"`
	CacheEnabled bool        `json:"cache_enabled"`
}

type DebankMultiCallResp struct {
	Results []*DebankSingleCallResult `json:"results"`
	Stats   *DebankMultiCallStats     `json:"stats"`
}

type DebankSimulateStats struct {
	BlockNum  uint64      `json:"block_num"`
	BlockHash common.Hash `json:"block_hash"`
	BlockTime int64       `json:"block_time"`
	Success   bool        `json:"success"`
}

type DebankSimulateResp struct {
	Results []evmtypes.DebankSingleSimulateResult `json:"results"`
	Stats   DebankSimulateStats                   `json:"stats"`
}
