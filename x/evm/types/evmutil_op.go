package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// EvmutilOpType identifies the evmutil conversion operation type.
type EvmutilOpType string

const (
	EvmutilOpMint   EvmutilOpType = "mint"   // CosmosCoin → ERC20 (calls mint on deployed ERC20)
	EvmutilOpBurn   EvmutilOpType = "burn"   // ERC20 → CosmosCoin (calls burn on deployed ERC20)
	EvmutilOpUnlock EvmutilOpType = "unlock" // Coin → ERC20 (transfer from module to user)
	EvmutilOpLock   EvmutilOpType = "lock"   // ERC20 → Coin (transfer from user to module)
)

// EvmutilModuleEVMAddress is the EVM address of kava's x/evmutil module account.
// = SHA256("evmutil")[:20]
var EvmutilModuleEVMAddress = common.HexToAddress("0x71586e5b3468b5720baa9162a02366fae6933bfe")

// evmutilReplayGasLimit is the gas limit used when replaying evmutil operations
// in TraceBlock. We use a large value since we only care about state changes,
// not gas accounting. The actual gas consumed doesn't matter for the state diff.
const evmutilReplayGasLimit = 30_000_000

// EvmutilOp describes a state-changing EVM call made by kava's evmutil module.
// These are reconstructed from Cosmos events and replayed in TraceBlock to
// capture EVM storage changes that are invisible to the normal EVM tracer.
type EvmutilOp struct {
	Type          EvmutilOpType  `json:"type"`
	From          common.Address `json:"from"`
	To            common.Address `json:"to"`                      // ERC20 contract address
	Data          []byte         `json:"data"`                    // ABI-encoded calldata (mint/burn/transfer)
	DeployData    []byte         `json:"deploy_data,omitempty"`   // contract creation bytecode (only for mint, used if first deploy)
	Nonce         uint64         `json:"nonce"`                   // sender nonce at execution time
	BlockTxIndex  int            `json:"block_tx_index"`          // position in original block tx list
}

// BuildEVMMessage converts an EvmutilOp into a core.Message suitable for
// ApplyMessageWithConfig. The message targets the ERC20 contract with the
// pre-built calldata.
func BuildEVMMessage(op EvmutilOp) ethtypes.Message {
	to := op.To
	return ethtypes.NewMessage(
		op.From,
		&to,
		op.Nonce,
		big.NewInt(0),             // value: 0
		evmutilReplayGasLimit,     // gasLimit
		big.NewInt(0),             // gasPrice
		big.NewInt(0),             // gasFeeCap
		big.NewInt(0),             // gasTipCap
		op.Data,                   // calldata
		ethtypes.AccessList{},     // accessList
		true,                      // isFake (skip signature verification)
	)
}

// BuildDeployMessage converts an EvmutilOp's DeployData into a contract
// creation message. Used when a Cosmos-native ERC20 is being deployed for
// the first time (no code at height N-1).
func BuildDeployMessage(op EvmutilOp) ethtypes.Message {
	return ethtypes.NewMessage(
		op.From,
		nil,                       // to: nil = contract creation
		op.Nonce,
		big.NewInt(0),             // value
		evmutilReplayGasLimit,     // gasLimit
		big.NewInt(0),             // gasPrice
		big.NewInt(0),             // gasFeeCap
		big.NewInt(0),             // gasTipCap
		op.DeployData,             // contract bytecode + constructor args
		ethtypes.AccessList{},
		true,                      // isFake
	)
}
