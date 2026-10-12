package spacetimedb

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const nativePublicationWait = 15 * time.Second

func publicationBytesSHA256(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
