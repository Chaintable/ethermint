package debank

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/evmos/ethermint/rpc/backend"
	rpctypes "github.com/evmos/ethermint/rpc/types"
	evmtypes "github.com/evmos/ethermint/x/evm/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

const (
	singleCallTimeout = 5 * time.Second
	multiCallLimit    = 50

	// client param error
	errInvalidParams = -32602
	errInternalError = -32603
	errEvmFailed     = -39004
)

const (
	nativeAddr = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

var (
	// copied from: accounts/abi/abi_test.go
	Uint8, _   = abi.NewType("uint8", "", nil)
	Uint256, _ = abi.NewType("uint256", "", nil)
	String, _  = abi.NewType("string", "", nil)
	Address, _ = abi.NewType("address", "", nil)

	erc20ABI = abi.ABI{
		Methods: map[string]abi.Method{
			"name":        funcName,
			"symbol":      funcSymbol,
			"decimals":    funcDecimals,
			"totalSupply": funcTotalSupply,
			"balanceOf":   funcBalanceOf,
		},
	}

	funcName = abi.NewMethod("name", "name", abi.Function, "", false, false,
		[]abi.Argument{},
		[]abi.Argument{
			{Name: "", Type: String, Indexed: false},
		},
	)
	funcSymbol = abi.NewMethod("symbol", "symbol", abi.Function, "", false, false,
		[]abi.Argument{},
		[]abi.Argument{
			{Name: "", Type: String, Indexed: false},
		},
	)
	funcDecimals = abi.NewMethod("decimals", "decimals", abi.Function, "", false, false,
		[]abi.Argument{},
		[]abi.Argument{
			{Name: "", Type: Uint8, Indexed: false},
		},
	)
	funcTotalSupply = abi.NewMethod("totalSupply", "totalSupply", abi.Function, "", false, false,
		[]abi.Argument{},
		[]abi.Argument{
			{Name: "", Type: Uint256, Indexed: false},
		},
	)
	funcBalanceOf = abi.NewMethod("balanceOf", "balanceOf", abi.Function, "", false, false,
		[]abi.Argument{
			{Name: "", Type: Address, Indexed: false},
		},
		[]abi.Argument{
			{Name: "", Type: Uint256, Indexed: false},
		},
	)
)

func handleNative(ctx context.Context, backend backend.EVMBackend, blockNrOrHash rpctypes.BlockNumberOrHash, arg evmtypes.TransactionArgs) ([]byte, int, error) {
	data := arg.GetData()
	method, err := erc20ABI.MethodById(data)
	if err != nil {
		return nil, errInvalidParams, err
	}
	switch method.Name {
	case "name", "symbol":
		res, err := method.Outputs.Pack("KAVA")
		if err != nil {
			return nil, errInternalError, err
		}
		return res, 0, nil
	case "decimals":
		res, err := method.Outputs.Pack(uint8(18))
		if err != nil {
			return nil, errInternalError, err
		}
		return res, 0, nil
	case "totalSupply":
		res, err := method.Outputs.Pack(big.NewInt(1_000_000_000_000_000_000)) //
		if err != nil {
			return nil, errInternalError, err
		}
		return res, 0, nil
	case "balanceOf":
		inputs, err := method.Inputs.Unpack(data[4:])
		if err != nil || len(inputs) == 0 {
			return nil, errInvalidParams, err
		}
		address, ok := inputs[0].(common.Address)
		if !ok {
			return nil, errInvalidParams, fmt.Errorf("input address error")
		}
		balanceInt, err := backend.GetBalance(address, blockNrOrHash)
		if err != nil {
			return nil, errInternalError, err
		}
		balance, err := method.Outputs.Pack(balanceInt.ToInt())
		if err != nil {
			return nil, errInternalError, err
		}
		return balance, 0, nil
	default:
		return nil, errInvalidParams, fmt.Errorf("method not found")
	}
}

func doOneCall(backend backend.EVMBackend, blockNrOrHash rpctypes.BlockNumberOrHash, arg evmtypes.TransactionArgs) (*rpctypes.DebankSingleCallResult, error) {
	var err error
	var result = &rpctypes.DebankSingleCallResult{}

	start := time.Now()

	// make sure this will be called prior to the SetCallCache defer func on returning
	defer func() {
		result.TimeCost = time.Since(start).Seconds()
	}()

	// skip EVM if requests for native token
	if strings.ToLower(arg.To.Hex()) == nativeAddr {
		res, code, err := handleNative(context.Background(), backend, blockNrOrHash, arg)
		if err != nil {
			result.Code = code
			result.Err = err.Error()
		}
		result.Result = res
		return result, err
	}

	blockNum, err := backend.BlockNumberFromTendermint(blockNrOrHash)
	if err != nil {
		result.Code = errInternalError
		result.Err = err.Error()
		return result, err
	}

	r, err := backend.DoCall(arg, blockNum)
	if err != nil {
		result.Code = errEvmFailed
		result.Err = err.Error()
		return result, err
	}

	result.Result = r.Ret
	result.GasUsed = int64(r.GasUsed)

	return result, nil
}

func (a *API) ContractMultiCall(
	args []evmtypes.TransactionArgs,
	blockContext *rpctypes.DebankBlockContext,
	_ *rpctypes.BlockOverrides,
	_ *rpctypes.StateOverride,
	pfastFail,
	puseParallel,
	pdisableCache *bool,
) (resp *rpctypes.DebankMultiCallResp, err error) {
	latestBlockNumber := rpctypes.EthLatestBlockNumber
	blockNrOrHash := rpctypes.BlockNumberOrHash{BlockNumber: &latestBlockNumber}
	if blockContext != nil {
		blockNrOrHash = blockContext.GetBlockNumberOrHash()
	}
	a.logger.Debug("eth_multiCall", "args", args, "block number or hash", blockNrOrHash)

	// maximum calls check
	if len(args) > multiCallLimit {
		return nil, fmt.Errorf("calls exceed limit, expected: <%v, actual: %v", multiCallLimit, len(args))
	}

	setb := func(p *bool, d bool) bool {
		if p == nil {
			return d
		}
		return *p
	}

	fastFail := setb(pfastFail, true)
	disableCache := setb(pdisableCache, false)
	useParallel := setb(puseParallel, false)

	ret := make([]*rpctypes.DebankSingleCallResult, len(args))
	stats := &rpctypes.DebankMultiCallStats{
		Success:      true,
		CacheEnabled: !disableCache,
	}

	blockNum, err := a.backend.BlockNumberFromTendermint(blockNrOrHash)
	if err == nil {
		tmBlock, err := a.backend.TendermintBlockByNumber(blockNum)
		if err == nil {
			stats.BlockNum = uint64(tmBlock.Block.Height)
			stats.BlockHash = common.BytesToHash(tmBlock.Block.Hash())
			stats.BlockTime = tmBlock.Block.Time.Unix()
		}
	}

	if useParallel {
		// run in parallel
		var wg sync.WaitGroup
		for i, arg := range args {
			wg.Add(1)
			go func(i int, arg evmtypes.TransactionArgs) {
				defer func() {
					if r := recover(); r != nil {
						a.logger.Error("RPC method eth_multiCall crashed: ", fmt.Sprintf("%v\n%s", err, r))
					}
				}()
				defer wg.Done()

				r, _ := doOneCall(a.backend, blockNrOrHash, arg)
				ret[i] = r
				if r.Err != "" {
					stats.Success = false
				}
			}(i, arg)
		}
		wg.Wait()

		return &rpctypes.DebankMultiCallResp{Results: ret, Stats: stats}, nil
	}

	// run in sequence
	failedOnce := false
	for i, arg := range args {
		if failedOnce {
			ret[i] = &rpctypes.DebankSingleCallResult{}
			continue
		}

		r, _ := doOneCall(a.backend, blockNrOrHash, arg)
		ret[i] = r
		if r.Err != "" {
			stats.Success = false
			if fastFail {
				failedOnce = true
			}
			continue
		}
	}
	return &rpctypes.DebankMultiCallResp{Results: ret, Stats: stats}, nil
}
