package statedb

import (
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

type (
	OnAccountSet    = func(addr common.Address, account Account)
	OnAccountDelete = func(addr common.Address)
	OnStateSet      = func(addr common.Address, key common.Hash, value []byte)
	OnCodeSet       = func(codeHash []byte, code []byte)
	LogHook         = func(log *ethtypes.Log)
)

type Hooks struct {
	OnAccountSet    OnAccountSet
	OnAccountDelete OnAccountDelete
	OnStateSet      OnStateSet
	OnCodeSet       OnCodeSet
	OnLog           LogHook
}
