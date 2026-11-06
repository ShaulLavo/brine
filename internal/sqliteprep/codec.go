package sqliteprep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type record interface{ Validate() error }

// Encode is the only canonical encoding. Decode compares the complete encoding,
// rejecting aliases, duplicate/unknown/missing fields, nulls, trailing values and
// noncanonical numbers at every depth, including embedded data/restore records.
func Encode(v record) ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(raw) > MaxRecordBytes {
		return nil, ErrLimit
	}
	return raw, nil
}
func Fingerprint(v record) (string, error) {
	raw, err := Encode(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func decode[T record](raw []byte, v T) (T, error) {
	if len(raw) == 0 || len(raw) > MaxRecordBytes {
		return v, ErrLimit
	}
	if json.Unmarshal(raw, v) != nil {
		return v, ErrInvalid
	}
	canonical, err := Encode(v)
	if err != nil {
		return v, err
	}
	if !bytes.Equal(canonical, raw) {
		return v, ErrInvalid
	}
	return v, nil
}
func DecodeInput(raw []byte) (Input, error)   { v, err := decode(raw, &Input{}); return *v, err }
func DecodeIntent(raw []byte) (Intent, error) { v, err := decode(raw, &Intent{}); return *v, err }
func DecodeEvent(raw []byte) (Event, error)   { v, err := decode(raw, &Event{}); return *v, err }
func DecodePreparedPoint(raw []byte) (PreparedPoint, error) {
	v, err := decode(raw, &PreparedPoint{})
	return *v, err
}
func DecodeReceipt(raw []byte) (Receipt, error)   { v, err := decode(raw, &Receipt{}); return *v, err }
func DecodeConsumer(raw []byte) (Consumer, error) { v, err := decode(raw, &Consumer{}); return *v, err }
func DecodeAbandonStop(raw []byte) (AbandonStop, error) {
	v, err := decode(raw, &AbandonStop{})
	return *v, err
}
