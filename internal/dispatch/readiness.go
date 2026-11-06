package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ReadinessArgs struct{}
type ReadinessReader interface {
	Read(context.Context) (readiness.Report, error)
}

func decodeReadiness(raw json.RawMessage) (any, error) {
	_, err := strictjson.Object(raw)
	return ReadinessArgs{}, err
}

func decodeReadinessEnvelope(command string, raw []byte) (result.Envelope, error) {
	report, err := readiness.DecodeReport(raw)
	if err != nil {
		return result.Envelope{}, err
	}
	return result.Success(command, report), nil
}
