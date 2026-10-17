package cloudflare_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cloudflare"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
)

type noOpWireHost struct {
	cdnoperator.Host
	plan   cdn.Plan
	effect cdn.Effect
}

func (h *noOpWireHost) progress(work cdn.Work) (cdnoperator.Progress, error) {
	raw, err := json.Marshal(cdnoperator.Progress{Operation: h.effect.Permit.Operation, Plan: h.plan, Work: work})
	if err != nil {
		return cdnoperator.Progress{}, err
	}
	var decoded cdnoperator.Progress
	err = json.Unmarshal(raw, &decoded)
	return decoded, err
}

func (h *noOpWireHost) Apply(context.Context, cdnoperator.ApplyRequest) (cdnoperator.Progress, error) {
	return h.progress(cdn.NeedEffect{Permit: h.effect.Permit})
}

func (h *noOpWireHost) Next(context.Context, cdnoperator.OperationRequest) (cdnoperator.Progress, error) {
	return h.progress(h.effect.Next())
}

func (h *noOpWireHost) RecordResult(_ context.Context, request cdnoperator.ResultRequest) (cdnoperator.Progress, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return cdnoperator.Progress{}, err
	}
	var decoded cdnoperator.ResultRequest
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return cdnoperator.Progress{}, err
	}
	h.effect, err = h.effect.Record(decoded.Result)
	if err != nil {
		return cdnoperator.Progress{}, err
	}
	return h.progress(h.effect.Next())
}

func (h *noOpWireHost) RecordInspection(_ context.Context, request cdnoperator.InspectionRequest) (cdnoperator.Progress, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return cdnoperator.Progress{}, err
	}
	var decoded cdnoperator.InspectionRequest
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return cdnoperator.Progress{}, err
	}
	h.effect, _, err = h.effect.Inspect(decoded.Inspection)
	if err != nil {
		return cdnoperator.Progress{}, err
	}
	return h.progress(cdn.Waiting{Code: "exposure_checked"})
}

type noOpWireDrivers struct {
	cdnoperator.Drivers
	edge cdn.EdgeDriver
}

func (d noOpWireDrivers) Edge(cdn.ProfileScope) (cdn.EdgeDriver, error) { return d.edge, nil }

func TestCloudflareNoOpAfterUploadCrossesProductionWireWithoutWrite(t *testing.T) {
	t.Run("apply", func(t *testing.T) { runNoOpWire(t, false) })
	t.Run("resume_indeterminate", func(t *testing.T) { runNoOpWire(t, true) })
}

func runNoOpWire(t *testing.T, resume bool) {
	t.Helper()
	edge, plan, permit, writes := cloudflare.ExposureNoOpWireFixture(t)
	host := &noOpWireHost{plan: plan, effect: cdn.Effect{Permit: permit, Stage: cdn.Issued}}
	coordinator := cdnoperator.Coordinator{Host: host, Drivers: noOpWireDrivers{edge: edge}}
	before := writes()
	var err error
	if resume {
		host.effect.Stage = cdn.Indeterminate
		_, err = coordinator.Resume(t.Context(), permit.Operation)
	} else {
		_, err = coordinator.Apply(t.Context(), cdnoperator.ApplyRequest{PlanID: plan.ID, IdempotencyKey: "enable"})
	}
	if err != nil || host.effect.Stage != cdn.Confirmed || writes() != before {
		t.Fatalf("no-op failed or wrote native state: stage=%s writes=%d->%d err=%v", host.effect.Stage, before, writes(), err)
	}
}
