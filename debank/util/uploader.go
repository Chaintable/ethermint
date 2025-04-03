package util

import (
	"bytes"
	"context"
	"cosmossdk.io/log"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"sync"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/segmentio/kafka-go"
	dtypes "github.com/zeta-chain/ethermint/debank/types"
	rpctypes "github.com/zeta-chain/ethermint/rpc/types"
)

func NewS3Client(region string) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, err
	}
	cfg.Region = region
	client := s3.NewFromConfig(cfg)
	return client, nil
}

func UploadFileToS3(uploader *s3.Client, bucket string, key string, data []byte) error {
	_, err := uploader.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

type Uploader struct {
	client          *s3.Client
	KafkaReader     *kafka.Reader
	KafkaWriter     *kafka.Writer
	LastBlockNotice *dtypes.BlockChangeNotification
	nodexBucket     string
	outerBucket     string
	chainID         string
}

func GetLastBlockNotice(reader *kafka.Reader) (*dtypes.BlockChangeNotification, error) {
	reader.SetOffset(0)
	lag, err := reader.ReadLag(context.Background())
	if err != nil {
		return nil, err
	}
	if lag == 0 {
		return nil, nil
	}

	err = reader.SetOffset(lag - 1)
	if err != nil {
		return nil, err
	}

	msg, err := reader.ReadMessage(context.Background())
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(msg.Key, []byte("NewBlock")) {
		return nil, fmt.Errorf("last message is not NewBlock")
	}

	blockNotice := &dtypes.BlockChangeNotification{}
	err = DecodeFromGzipJson(msg.Value, blockNotice)
	if err != nil {
		return nil, err
	}

	return blockNotice, nil
}

func NewUploader(region string, nodeXBucket, chainTableBucket string, broker string, topic string, chainID string) (*Uploader, error) {
	client, err := NewS3Client(region)
	if err != nil {
		return nil, err
	}
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: []string{broker},
		Topic:   topic,
		GroupID: "",
	})
	writer := kafka.NewWriter(kafka.WriterConfig{
		Brokers:      []string{broker},
		Topic:        topic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: 1,
		// 默认100个，或者等待1s才发生
		BatchSize: 1,
	})
	LastBlockNotice, err := GetLastBlockNotice(reader)
	if err != nil {
		return nil, err
	}
	uploader := &Uploader{
		client:          client,
		nodexBucket:     nodeXBucket,
		outerBucket:     chainTableBucket,
		KafkaReader:     reader,
		KafkaWriter:     writer,
		LastBlockNotice: LastBlockNotice,
		chainID:         chainID,
	}
	return uploader, nil
}

func (u *Uploader) Close() {
	u.KafkaReader.Close()
	u.KafkaWriter.Close()
}

func (k *Uploader) Logger(ctx sdk.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger()
}
func (u *Uploader) UploadDebankOutPut(ctx sdk.Context, out *rpctypes.DebankOutPut) error {
	wg := sync.WaitGroup{}

	var allerr error
	var lock sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := u.uploadBlockFile(out.BlockFile)
		if err != nil {
			lock.Lock()
			allerr = err
			lock.Unlock()
			u.Logger(ctx).Error("uploadBlockFile", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := u.uploadFileValidation(u.chainID, out.BlockFile)
		if err != nil {
			lock.Lock()
			allerr = err
			lock.Unlock()
			u.Logger(ctx).Error("uploadFileValidation", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := u.uploadHeader(u.chainID, out.Header)
		if err != nil {
			lock.Lock()
			allerr = err
			lock.Unlock()
			u.Logger(ctx).Error("uploadHeader", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := u.uploadStateDiff(u.chainID, out.StateDiff)
		if err != nil {
			lock.Lock()
			allerr = err
			lock.Unlock()
			u.Logger(ctx).Error("uploadStateDiff", "err", err)
		}
	}()
	wg.Wait()

	u.Logger(ctx).Info("upload debank output", "block", out.Header.Number.ToInt().Uint64())

	return allerr
}

func (u *Uploader) PushDebankOutPut(ctx sdk.Context, out *rpctypes.DebankOutPut) error {
	if u.LastBlockNotice == nil {
		if out == nil {
			return fmt.Errorf("out is nil")
		}
		if out.Header.Number.ToInt().Uint64() == 1 {
			lastBlockNotice := &dtypes.BlockChangeNotification{
				ChangeType: 1,
				NewBlocks: []dtypes.BlockContext{
					{
						Hash:        out.Header.Hash,
						ParentHash:  out.Header.ParentHash,
						BlockNumber: out.Header.Number.ToInt().Uint64(),
						Timestamp:   uint64(out.Header.Timestamp),
					},
				},
			}
			err := u.WriteBlockNotice(lastBlockNotice)
			u.Logger(ctx).Info("write block notice", "block", out.Header.Number.ToInt().Uint64())
			if err != nil {
				return err
			}
			u.LastBlockNotice = lastBlockNotice
		}
	} else {
		lastBlock := u.LastBlockNotice.NewBlocks[len(u.LastBlockNotice.NewBlocks)-1]
		if out.Header.Number.ToInt().Uint64() <= lastBlock.BlockNumber {
			return nil
		} else if out.Header.Number.ToInt().Uint64() == lastBlock.BlockNumber+1 {
			if out.Header.ParentHash != lastBlock.Hash {
				u.Logger(ctx).Error("parent hash not match", "header", out.Header, "lastBlock", lastBlock)
				return fmt.Errorf("parent hash not match")
			}
			lastBlockNotice := &dtypes.BlockChangeNotification{
				ChangeType: 1,
				NewBlocks: []dtypes.BlockContext{
					{
						Hash:        out.Header.Hash,
						ParentHash:  out.Header.ParentHash,
						BlockNumber: out.Header.Number.ToInt().Uint64(),
						Timestamp:   uint64(out.Header.Timestamp),
					},
				},
			}
			err := u.WriteBlockNotice(lastBlockNotice)
			if err != nil {
				return err
			}
			u.LastBlockNotice = lastBlockNotice
			u.Logger(ctx).Info("write block notice", "block", out.Header.Number.ToInt().Uint64())
		} else {
			u.Logger(ctx).Error("block number not match", "header", out.Header, "lastBlock", lastBlock)
			return fmt.Errorf("block number not match")
		}
	}
	return nil

}

// s3key: chain_id/block_id
// 外部s3
func (u *Uploader) uploadBlockFile(blockFile *dtypes.BlockFile) error {
	data, err := EncodeToJsonGzip(blockFile)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%s", u.chainID, blockFile.Block.ID)
	_, err = u.client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &u.outerBucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

// s3key: chain_id/block_height/block_id
// 外部s3,empty object,只用key
func (u *Uploader) uploadFileValidation(chainID string, blockFile *dtypes.BlockFile) error {
	data, err := EncodeToJsonGzip(blockFile.Validation())
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%d/%s", chainID, blockFile.Block.Height, blockFile.Block.ID)
	_, err = u.client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &u.outerBucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

// s3Key: <chainID>/<blockHash>/block
// 内部s3
func (u *Uploader) uploadHeader(chainID string, header *dtypes.Header) error {
	data, err := EncodeToJsonGzip(header)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%s/block", chainID, header.Hash.String())
	_, err = u.client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &u.nodexBucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

// s3Key: <chainID>/<blockHash>/stateDiff
// 内部s3
func (u *Uploader) uploadStateDiff(chainID string, stateDiff *dtypes.BlockStateDiff) error {
	data, err := EncodeToRlp(stateDiff)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%s/stateDiff", chainID, stateDiff.Hash.Hex())
	_, err = u.client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &u.nodexBucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	return err
}

func (u *Uploader) WriteBlockNotice(blockNotice *dtypes.BlockChangeNotification) error {
	value, err := EncodeToJsonGzip(blockNotice)
	if err != nil {
		return err
	}
	err = u.KafkaWriter.WriteMessages(context.Background(), kafka.Message{
		Key:   []byte("NewBlock"),
		Value: value,
	})
	if err != nil {
		return err
	}
	return nil
}
