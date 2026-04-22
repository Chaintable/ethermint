package statedb

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	dtypes "github.com/evmos/ethermint/debank/types"
)

// Ensure StateDiffCollector satisfies vm.EVMLogger at compile time.
var _ vm.EVMLogger = (*StateDiffCollector)(nil)

// StateDiffCollector is a lightweight state diff recorder that captures
// account, storage and code changes via StateDB hooks. It implements
// HooksProvider so that ApplyMessageWithConfig wires the hooks automatically.
//
// It is designed to be used as the tracer parameter to ApplyMessage in places
// where NoOpTracer was previously used (e.g. evmutil.CallEVMWithData), enabling
// state change capture without a full CallTracer.
type StateDiffCollector struct {
	newAccounts     map[common.Hash]Account
	deletedAccounts map[common.Hash]struct{}
	storageDiff     map[common.Hash]map[common.Hash][]byte // addrHash -> slotHash -> value
	newCodes        map[common.Hash][]byte                  // codeHash -> code
	storageChanges  map[common.Address]struct{}             // raw addresses with storage changes
}

// NewStateDiffCollector creates a ready-to-use collector.
func NewStateDiffCollector() *StateDiffCollector {
	return &StateDiffCollector{
		newAccounts:     make(map[common.Hash]Account),
		deletedAccounts: make(map[common.Hash]struct{}),
		storageDiff:     make(map[common.Hash]map[common.Hash][]byte),
		newCodes:        make(map[common.Hash][]byte),
		storageChanges:  make(map[common.Address]struct{}),
	}
}

// GetHooks implements HooksProvider.
func (c *StateDiffCollector) GetHooks() *Hooks {
	return &Hooks{
		OnAccountSet:    c.onAccountSet,
		OnAccountDelete: c.onAccountDelete,
		OnStateSet:      c.onStateSet,
		OnCodeSet:       c.onCodeSet,
	}
}

func (c *StateDiffCollector) onAccountSet(addr common.Address, account Account) {
	c.newAccounts[crypto.Keccak256Hash(addr.Bytes())] = account
}

func (c *StateDiffCollector) onAccountDelete(addr common.Address) {
	c.deletedAccounts[crypto.Keccak256Hash(addr.Bytes())] = struct{}{}
}

func (c *StateDiffCollector) onStateSet(addr common.Address, key common.Hash, value []byte) {
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	if _, ok := c.storageDiff[addrHash]; !ok {
		c.storageDiff[addrHash] = make(map[common.Hash][]byte)
	}
	c.storageDiff[addrHash][crypto.Keccak256Hash(key.Bytes())] = value
	c.storageChanges[addr] = struct{}{}
}

func (c *StateDiffCollector) onCodeSet(codeHash []byte, code []byte) {
	c.newCodes[common.BytesToHash(codeHash)] = code
}

// ToStateDiff converts collected state changes into a TransactionStateDiff.
func (c *StateDiffCollector) ToStateDiff() dtypes.TransactionStateDiff {
	diff := dtypes.TransactionStateDiff{
		NewAccounts:     make([]dtypes.NewAccount, 0, len(c.newAccounts)),
		DeletedAccounts: make([]common.Hash, 0, len(c.deletedAccounts)),
		StorageDiff:     make([]dtypes.AccountStorageDiff, 0, len(c.storageDiff)),
		NewCodes:        make([]dtypes.NewCode, 0, len(c.newCodes)),
	}

	for addrHash, acc := range c.newAccounts {
		balance := uint256.NewInt(0)
		if acc.Balance != nil {
			balance = uint256.NewInt(0).SetBytes(acc.Balance.Bytes())
		}
		diff.NewAccounts = append(diff.NewAccounts, dtypes.NewAccount{
			Address:  addrHash,
			Balance:  balance,
			Nonce:    acc.Nonce,
			CodeHash: common.BytesToHash(acc.CodeHash),
		})
	}

	for addrHash := range c.deletedAccounts {
		diff.DeletedAccounts = append(diff.DeletedAccounts, addrHash)
	}

	for addrHash, slots := range c.storageDiff {
		pairs := make([]dtypes.IndexValuePair, 0, len(slots))
		for slotHash, value := range slots {
			v := uint256.NewInt(0)
			if len(value) > 0 {
				v = uint256.NewInt(0).SetBytes(value)
			}
			pairs = append(pairs, dtypes.IndexValuePair{Index: slotHash, Value: v})
		}
		diff.StorageDiff = append(diff.StorageDiff, dtypes.AccountStorageDiff{
			Address: addrHash,
			Values:  pairs,
		})
	}

	for codeHash, code := range c.newCodes {
		diff.NewCodes = append(diff.NewCodes, dtypes.NewCode{
			CodeHash: codeHash,
			Code:     code,
		})
	}

	return diff
}

// AddStorageChange adds a storage slot change to the collector.
// Used by ForEachStorage enumeration for newly deployed contracts.
func (c *StateDiffCollector) AddStorageChange(addr common.Address, key common.Hash, value []byte) {
	c.onStateSet(addr, key, value)
}

// IsEmpty reports whether no state changes were collected.
func (c *StateDiffCollector) IsEmpty() bool {
	return len(c.newAccounts) == 0 &&
		len(c.deletedAccounts) == 0 &&
		len(c.storageDiff) == 0 &&
		len(c.newCodes) == 0
}

// vm.EVMLogger no-op implementations

func (c *StateDiffCollector) CaptureStart(_ *vm.EVM, _ common.Address, _ common.Address, _ bool, _ []byte, _ uint64, _ *big.Int) { //nolint: revive
}
func (c *StateDiffCollector) CaptureState(_ uint64, _ vm.OpCode, _, _ uint64, _ *vm.ScopeContext, _ []byte, _ int, _ error) { //nolint: revive
}
func (c *StateDiffCollector) CaptureFault(_ uint64, _ vm.OpCode, _, _ uint64, _ *vm.ScopeContext, _ int, _ error) { //nolint: revive
}
func (c *StateDiffCollector) CaptureEnd(_ []byte, _ uint64, _ time.Duration, _ error) {} //nolint: revive
func (c *StateDiffCollector) CaptureEnter(_ vm.OpCode, _ common.Address, _ common.Address, _ []byte, _ uint64, _ *big.Int) { //nolint: revive
}
func (c *StateDiffCollector) CaptureExit(_ []byte, _ uint64, _ error) {} //nolint: revive
func (c *StateDiffCollector) CaptureTxStart(_ uint64)                  {} //nolint: revive
func (c *StateDiffCollector) CaptureTxEnd(_ uint64)                    {} //nolint: revive
