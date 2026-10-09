package apps

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

var appPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func DecodeReport(raw []byte) (Report, error) {
	var report Report
	if exact(raw, reflect.TypeFor[Report]()) != nil || json.Unmarshal(raw, &report) != nil {
		return Report{}, strictjson.ErrObject
	}
	seen := map[string]bool{}
	for _, a := range report.Apps {
		if !appPattern.MatchString(a.App) || seen[a.App] || !validRelease(a.Current) || a.Previous != nil && !validRelease(*a.Previous) {
			return Report{}, strictjson.ErrObject
		}
		seen[a.App] = true
		if a.LastOperation != nil && (!idPattern.MatchString(a.LastOperation.ID) || !ops.ValidOperation(*a.LastOperation)) {
			return Report{}, strictjson.ErrObject
		}
		h := a.Health.UnitActive
		if h.Status != target.KnownStatus && h.Status != target.Unknown || (h.Status == target.KnownStatus) != (h.Value != nil) || a.Health.Direct != "not_checked" && a.Health.Direct != "healthy" && a.Health.Direct != "unhealthy" {
			return Report{}, strictjson.ErrObject
		}
		if a.Drift.State != "in_sync" && a.Drift.State != "drifted" && a.Drift.State != "unknown" {
			return Report{}, strictjson.ErrObject
		}
		for _, field := range a.Drift.Fields {
			switch field {
			case "app", "image", "port", "units", "secrets", "caddy", "domains":
			default:
				return Report{}, strictjson.ErrObject
			}
		}
	}
	return report, nil
}
func validRelease(r Release) bool {
	if !idPattern.MatchString(r.ID) || !digestPattern.MatchString(r.PlanID) || !digestPattern.MatchString(r.ImageDigest) || r.Port == 0 || r.Port > 65535 {
		return false
	}
	for _, d := range r.Domains {
		canonical, e := target.CanonicalDomain(string(d))
		if e != nil || canonical != string(d) {
			return false
		}
	}
	return true
}
func DecodeRollback(raw []byte) (RollbackPlan, error) {
	var p RollbackPlan
	if exact(raw, reflect.TypeFor[RollbackPlan]()) != nil || json.Unmarshal(raw, &p) != nil || !digestPattern.MatchString(p.PlanID) || !idPattern.MatchString(p.ReleaseID) || p.Compatibility != "stateless_compatible" || p.Kind != plan.Update && p.Kind != plan.Conflict {
		return RollbackPlan{}, strictjson.ErrObject
	}
	if p.Kind == plan.Update && len(p.Conflicts) != 0 || p.Kind == plan.Conflict && len(p.Conflicts) == 0 {
		return RollbackPlan{}, strictjson.ErrObject
	}
	return p, nil
}

// Response types own exact field names, including optional diff fields. Walking
// their tags also rejects duplicates and aliases in nested payloads.
func exact(raw []byte, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return exact(raw, t.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return strictjson.ErrObject
	}
	if t == reflect.TypeFor[time.Time]() {
		_, e := strictjson.Value[time.Time](raw)
		return e
	}
	switch t.Kind() {
	case reflect.Struct:
		var keys map[string]json.RawMessage
		if json.Unmarshal(raw, &keys) != nil || keys == nil {
			return strictjson.ErrObject
		}
		fields := map[string]reflect.StructField{}
		names := []string{}
		for f := range t.Fields() {
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			fields[tag[0]] = f
			if len(tag) == 1 {
				if _, ok := keys[tag[0]]; !ok {
					return strictjson.ErrObject
				}
			}
		}
		for name := range keys {
			if _, ok := fields[name]; !ok {
				return strictjson.ErrObject
			}
			names = append(names, name)
		}
		values, e := strictjson.Object(raw, names...)
		if e != nil {
			return e
		}
		for name, value := range values {
			if e := exact(value, fields[name].Type); e != nil {
				return e
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return strictjson.ErrObject
		}
		for _, v := range values {
			if e := exact(v, t.Elem()); e != nil {
				return e
			}
		}
	default:
		value := reflect.New(t)
		if json.Unmarshal(raw, value.Interface()) != nil {
			return strictjson.ErrObject
		}
	}
	return nil
}

func DecodeConfigPlan(raw []byte) (ConfigPlan, error) {
	var p ConfigPlan
	if exact(raw, reflect.TypeFor[ConfigPlan]()) != nil || json.Unmarshal(raw, &p) != nil || !digestPattern.MatchString(p.PlanID) || p.Kind != plan.Update && p.Kind != plan.NoOp && p.Kind != plan.Conflict {
		return ConfigPlan{}, strictjson.ErrObject
	}
	if p.Lifecycle != "" && p.Lifecycle != plan.RestartApp && p.Lifecycle != plan.StopApp && p.Lifecycle != plan.StartApp {
		return ConfigPlan{}, strictjson.ErrObject
	}
	if p.Kind == plan.Conflict && len(p.Conflicts) == 0 || p.Kind != plan.Conflict && len(p.Conflicts) != 0 {
		return ConfigPlan{}, strictjson.ErrObject
	}
	return p, nil
}
