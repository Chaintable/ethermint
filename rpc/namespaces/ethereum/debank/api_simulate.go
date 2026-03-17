package debank

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"

	rpctypes "github.com/evmos/ethermint/rpc/types"
)

type CallArgs struct {
	From     *common.Address `json:"from"`
	To       *common.Address `json:"to"`
	Gas      *hexutil.Uint64 `json:"gas"`
	GasPrice *hexutil.Big    `json:"gasPrice"`
	Value    *hexutil.Big    `json:"value"`
	Data     *hexutil.Bytes  `json:"data"`
	Nonce    *hexutil.Uint64 `json:"nonce"`
	ChainID  *big.Int        `json:"chainId,omitempty"`
}

// SimulateTransactions batch simulates transactions against a specific block.
// Note: This is a simplified version for Kava. Full batch simulation requires
// custom EthCall support which is not available in standard ethermint.
func (a *API) SimulateTransactions(args []CallArgs, blockContext *rpctypes.DebankBlockContext, _ *rpctypes.BlockOverrides) (*rpctypes.DebankSimulateResp, error) {
	// TODO: implement when kava supports batch EthCall
	return nil, nil
}
