package cli

import (
	"context"
	"strings"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/result"
)

func diagnosisGoldenReport(app string, unknown bool) diagnose.Report {
	reader := diagnose.Reader{}
	if !unknown {
		reader = diagnosisTestServer().Diagnose.(diagnose.Reader)
	}
	report, err := reader.Read(context.Background(), diagnose.Request{App: app})
	if err != nil {
		panic(err)
	}
	return report
}

func diagnosisGoldenModel(report diagnose.Report) tuiModel {
	m := fixtureModel(fixtureHost(), "fixture")
	m.stopRead()
	m.screen, m.diagnosisFlags = screenDiagnosis, lifecycleSelection
	if len(report.Apps) == 1 {
		m.diagnosisApp = report.Apps[0].Name
	}
	m.diagnosis.finish(report, nil, fixtureReadTime)
	var err error
	m.diagnosisText, err = diagnosisLines(report, m.diagnosisFlags)
	if err != nil {
		panic(err)
	}
	return m
}

func tuiDiagnosisGoldenStates() map[string]tuiModel {
	ready := diagnosisGoldenModel(diagnosisGoldenReport("demo", false))
	loading := diagnosisGoldenModel(diagnose.Report{})
	loading.diagnosis, loading.diagnosisText = loadable[diagnose.Report]{State: loadLoading}, nil
	failed := loading
	failed.diagnosis.State, failed.diagnosis.Err = loadFailed, result.New(result.TransportFailure, nil)
	stale := ready
	stale.diagnosis.State, stale.diagnosis.Err = loadFailed, result.New(result.TransportInvalidResponse, nil)
	refresh := ready
	refresh.diagnosis.start()
	unknown := diagnosisGoldenModel(diagnosisGoldenReport("demo", true))
	empty := diagnosisGoldenModel(diagnosisGoldenReport("", true))
	truncatedReport := diagnosisGoldenReport("demo", false)
	truncatedReport.Truncated = true
	truncated := diagnosisGoldenModel(truncatedReport)
	truncated.diagnosisOffset = len(truncated.diagnosisText) - 22
	findings := ready
	findings.diagnosisOffset = len(findings.diagnosisText) - 14
	long := ready
	long.diagnosisFlags.target = strings.Repeat("target-", 8) + "name"
	long.diagnosisApp = "long-界\x1b]52;c;control\a" + strings.Repeat("app-", 12)
	return map[string]tuiModel{
		"diagnosis-loading": loading, "diagnosis-ready": ready, "diagnosis-findings": findings,
		"diagnosis-unknown": unknown, "diagnosis-empty": empty, "diagnosis-failed": failed,
		"diagnosis-stale": stale, "diagnosis-refreshing": refresh, "diagnosis-truncated": truncated,
		"diagnosis-long-controls": long,
	}
}
