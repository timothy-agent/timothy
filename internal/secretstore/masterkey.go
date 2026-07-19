package secretstore

import (
	"encoding/base64"
	"fmt"
)

// MasterKeyEnv is the env var holding the base64-encoded 32-byte
// master key. It is the one credential still allowed to be env-based:
// the root of trust that unlocks everything else in the DB-backed
// store.
const MasterKeyEnv = "TIMOTHY_MASTER_KEY"

// DecodeMasterKey base64-decodes a master key and checks its length.
func DecodeMasterKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set", MasterKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", MasterKeyEnv, err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("%s must decode to %d bytes, got %d", MasterKeyEnv, keyLen, len(key))
	}
	return key, nil
}
