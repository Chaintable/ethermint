package pipeline

import (
	"context"
	"cosmossdk.io/core/appmodule"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/zeta-chain/ethermint/x/pipeline/keeper"
	"github.com/zeta-chain/ethermint/x/pipeline/types"
)

var (
	_ module.AppModule      = AppModule{}
	_ module.AppModuleBasic = AppModuleBasic{}

	_ appmodule.HasEndBlocker = AppModule{}
)

type AppModuleBasic struct{}

func (a AppModuleBasic) Name() string {
	return types.ModuleName
}

func (a AppModuleBasic) RegisterLegacyAminoCodec(amino *codec.LegacyAmino) {

}

func (a AppModuleBasic) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
}

func (a AppModuleBasic) RegisterGRPCGatewayRoutes(context client.Context, mux *runtime.ServeMux) {
}

type AppModule struct {
	AppModuleBasic
	keeper keeper.Keeper
}

func (a AppModule) EndBlock(ctx context.Context) error {
	return a.keeper.EndBlock(ctx)
}

func (a AppModule) IsOnePerModuleType() {
}

func (a AppModule) IsAppModule() {
}
