package types

import "encoding/binary"

const (
	// ModuleName is the name of the pipeline module
	ModuleName = "pipeline"

	// StoreKey is the string store representation
	StoreKey = ModuleName

	// RouterKey is the msg router key for the pipeline module
	RouterKey = ModuleName
)

var (
	HistoricalInfoKey = []byte{0x11} // prefix for the historical info
)

// GetHistoricalInfoKey returns a key prefix for indexing HistoricalInfo objects.
func GetHistoricalInfoKey(height int64) []byte {
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, uint64(height))
	return append(HistoricalInfoKey, heightBytes...)
}
