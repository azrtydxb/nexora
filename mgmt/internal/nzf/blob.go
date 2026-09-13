package nzf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Compress wraps NZF bytes in one zstd frame (checksum on) and returns the lowercase hex SHA-256
// of the compressed bytes, which addresses the blob.
func Compress(raw []byte) (data []byte, sha256hex string, err error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(true), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, "", err
	}
	defer enc.Close()
	data = enc.EncodeAll(raw, nil)
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// Decompress inflates a zone blob, refusing output larger than maxSize octets.
func Decompress(data []byte, maxSize int) ([]byte, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("nzf: max size %d", maxSize)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(maxSize)), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	out, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("nzf: decompress: %w", err)
	}
	if len(out) > maxSize {
		return nil, fmt.Errorf("nzf: decompressed size %d exceeds %d", len(out), maxSize)
	}
	return out, nil
}
