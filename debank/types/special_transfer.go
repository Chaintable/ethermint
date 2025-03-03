package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common/hexutil"
)

// id = to_hash(transfer['block_id'], transfer['to_addr'], transfer['pos'])
type SpecialTransfer struct {
	ID          string       `json:"id"`
	FromAddress string       `json:"from_address"`
	ToAddress   string       `json:"to_address"`
	Value       *hexutil.Big `json:"value"`
	Memo        string       `json:"memo"` // block_reward / gasfee_reward / beacon_withdrawl
	Idx         *big.Int     `json:"pos"`  // block_reward / gasfee_reward = 0
}
