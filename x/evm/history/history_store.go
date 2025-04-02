package history

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/syndtr/goleveldb/leveldb"
)

const (
	historicalInfoPrefix = iota + 1
)

var (
	HistoricalInfoKey = []byte{historicalInfoPrefix}
)

var (
	ErrNoHistoricalInfo   = fmt.Errorf("no historical info found")
	ErrPipelineNotEnabled = fmt.Errorf("pipeline not enabled")
)

type Store struct {
	db *leveldb.DB
}

func NewHistoryStore(db *leveldb.DB) *Store {
	return &Store{
		db: db,
	}
}

func (s *Store) guard() error {
	if s.db == nil {
		return ErrPipelineNotEnabled
	}
	return nil
}

// GetHistoricalInfo gets the historical info at a given height
func (s *Store) GetHistoricalInfo(_ context.Context, height int64) (hi HistoricalInfo, err error) {
	if err = s.guard(); err != nil {
		return hi, err
	}
	key := historicalInfoKey(height)
	value, err := s.db.Get(key, nil)
	if err != nil {
		if errors.Is(err, leveldb.ErrNotFound) {
			return hi, ErrNoHistoricalInfo
		}
		return hi, err
	}
	if err = json.Unmarshal(value, &hi); err != nil {
		return hi, err
	}
	return hi, nil
}

// SetHistoricalInfo sets the historical info at a given height
func (s *Store) SetHistoricalInfo(_ context.Context, height int64, hi *HistoricalInfo) error {
	if err := s.guard(); err != nil {
		return err
	}
	key := historicalInfoKey(height)
	value, err := json.Marshal(hi)
	if err != nil {
		return err
	}
	return s.db.Put(key, value, nil)
}

// DeleteHistoricalInfo deletes the historical info at a given height
func (s *Store) DeleteHistoricalInfo(_ context.Context, height int64) error {
	if err := s.guard(); err != nil {
		return err
	}
	key := historicalInfoKey(height)
	return s.db.Delete(key, nil)
}

func historicalInfoKey(height int64) []byte {
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, uint64(height))
	return append(HistoricalInfoKey, heightBytes...)
}

type HistoricalInfo struct {
	Header     cmtproto.Header `json:"header"`
	HeaderHash []byte          `json:"header_hash"`
}
