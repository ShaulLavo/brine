package restic

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

var summaryFields = []string{"files_new", "files_changed", "files_unmodified", "dirs_new", "dirs_changed", "dirs_unmodified", "data_blobs", "tree_blobs", "data_added", "data_added_packed", "total_files_processed", "total_bytes_processed", "total_duration", "backup_start", "backup_end"}
var snapshotFields = []string{"time", "tree", "paths", "hostname", "username", "uid", "gid", "tags", "program_version", "summary", "id", "short_id"}

func wireObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, ErrIntegrity
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) {
			return nil, ErrIntegrity
		}
		keys = append(keys, key)
	}
	for _, key := range required {
		if fields[key] == nil || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return nil, ErrIntegrity
		}
	}
	if _, err := strictjson.Object(raw, keys...); err != nil {
		return nil, ErrIntegrity
	}
	return fields, nil
}

func messages(raw []byte) ([]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > jsonLimit {
		return nil, ErrIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var records []json.RawMessage
	for len(records) <= 64 {
		var record json.RawMessage
		err := decoder.Decode(&record)
		if err == io.EOF {
			return records, nil
		}
		if err != nil {
			return nil, ErrIntegrity
		}
		records = append(records, record)
	}
	return nil, ErrLimit
}

func parseInit(raw []byte) error {
	fields, err := wireObject(raw, []string{"message_type", "id", "repository"}, nil)
	if err != nil {
		return err
	}
	kind, err := strictjson.Value[string](fields["message_type"])
	if err != nil || kind != "initialized" {
		return ErrIntegrity
	}
	id, err := strictjson.Value[string](fields["id"])
	if err != nil || !hashPattern.MatchString(id) {
		return ErrIntegrity
	}
	location, err := strictjson.Value[string](fields["repository"])
	if err != nil || location == "" {
		return ErrIntegrity
	}
	return nil
}

func parseBackup(raw []byte, archiveBytes int64) (string, error) {
	records, err := messages(raw)
	if err != nil {
		return "", err
	}
	id := ""
	for _, record := range records {
		var kind struct {
			MessageType string `json:"message_type"`
		}
		if err := json.Unmarshal(record, &kind); err != nil || id != "" {
			return "", ErrIntegrity
		}
		if kind.MessageType == "status" {
			if _, err := wireObject(record, []string{"message_type"}, []string{"percent_done", "total_files", "files_done", "total_bytes", "bytes_done", "seconds_elapsed", "seconds_remaining", "current_files"}); err != nil {
				return "", err
			}
			continue
		}
		if kind.MessageType != "summary" {
			return "", ErrIntegrity
		}
		fields, err := wireObject(record, append([]string{"message_type", "snapshot_id"}, summaryFields...), nil)
		if err != nil {
			return "", err
		}
		id, err = strictjson.Value[string](fields["snapshot_id"])
		if err != nil || !hashPattern.MatchString(id) {
			return "", ErrIntegrity
		}
		if err := verifySummary(fields, archiveBytes); err != nil {
			return "", err
		}
	}
	if id == "" {
		return "", ErrIntegrity
	}
	return id, nil
}

func verifySummary(fields map[string]json.RawMessage, archiveBytes int64) error {
	for _, key := range summaryFields[:12] {
		if _, err := strictjson.Value[uint64](fields[key]); err != nil {
			return ErrIntegrity
		}
	}
	files, err := strictjson.Value[int64](fields["total_files_processed"])
	if err != nil || files != 1 {
		return ErrIntegrity
	}
	size, err := strictjson.Value[int64](fields["total_bytes_processed"])
	if err != nil || size != archiveBytes {
		return ErrIntegrity
	}
	duration, err := strictjson.Value[float64](fields["total_duration"])
	if err != nil || duration < 0 {
		return ErrIntegrity
	}
	for _, key := range []string{"backup_start", "backup_end"} {
		value, err := strictjson.Value[string](fields[key])
		if err != nil {
			return ErrIntegrity
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return ErrIntegrity
		}
	}
	return nil
}

func parseSnapshots(raw []byte, identity Identity) (string, error) {
	values, err := strictjson.Value[[]json.RawMessage](raw)
	if err != nil || len(values) != 1 {
		return "", ErrIntegrity
	}
	return parseSnapshot(values[0], identity, false)
}

func parseSnapshot(raw []byte, identity Identity, listing bool) (string, error) {
	required := slices.Clone(snapshotFields)
	if listing {
		required = append(required, "message_type", "struct_type")
	}
	fields, err := wireObject(raw, required, []string{"parent"})
	if err != nil {
		return "", err
	}
	var value struct {
		ID             string    `json:"id"`
		Tree           string    `json:"tree"`
		Paths          []string  `json:"paths"`
		Hostname       string    `json:"hostname"`
		Username       string    `json:"username"`
		UID            uint32    `json:"uid"`
		GID            uint32    `json:"gid"`
		Tags           []string  `json:"tags"`
		ProgramVersion string    `json:"program_version"`
		ShortID        string    `json:"short_id"`
		Parent         string    `json:"parent"`
		Time           time.Time `json:"time"`
		MessageType    string    `json:"message_type"`
		StructType     string    `json:"struct_type"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || !hashPattern.MatchString(value.ID) || !hashPattern.MatchString(value.Tree) || value.ShortID != value.ID[:8] || value.Hostname != "brine" || value.ProgramVersion != "restic "+Version || value.Time.IsZero() {
		return "", ErrIntegrity
	}
	if value.Parent != "" && !hashPattern.MatchString(value.Parent) {
		return "", ErrIntegrity
	}
	if !slices.Equal(value.Paths, []string{"/" + archiveName}) || !slices.Equal(value.Tags, []string{captureTag(identity)}) {
		return "", ErrIntegrity
	}
	if listing && (value.MessageType != "snapshot" || value.StructType != "snapshot") {
		return "", ErrIntegrity
	}
	if fields["summary"] == nil {
		return "", ErrIntegrity
	}
	return value.ID, nil
}

func parseListing(raw []byte, point TransportPoint) error {
	records, err := messages(raw)
	if err != nil || len(records) != 2 {
		return ErrIntegrity
	}
	id, err := parseSnapshot(records[0], point.Identity, true)
	if err != nil || id != point.SnapshotID {
		return ErrIntegrity
	}
	if _, err := wireObject(records[1], []string{"name", "type", "path", "uid", "gid", "size", "mode", "permissions", "mtime", "atime", "ctime", "message_type", "struct_type"}, nil); err != nil {
		return err
	}
	var node struct {
		Name        string    `json:"name"`
		Type        string    `json:"type"`
		Path        string    `json:"path"`
		Size        int64     `json:"size"`
		Mode        uint32    `json:"mode"`
		UID         uint32    `json:"uid"`
		GID         uint32    `json:"gid"`
		Permissions string    `json:"permissions"`
		Mtime       time.Time `json:"mtime"`
		Atime       time.Time `json:"atime"`
		Ctime       time.Time `json:"ctime"`
		MessageType string    `json:"message_type"`
		StructType  string    `json:"struct_type"`
	}
	if err := json.Unmarshal(records[1], &node); err != nil || node.Name != archiveName || node.Type != "file" || node.Path != "/"+archiveName || node.Size != point.ArchiveBytes || node.MessageType != "node" || node.StructType != "node" {
		return ErrIntegrity
	}
	return nil
}

func parseCheck(raw []byte) error {
	fields, err := wireObject(raw, []string{"message_type", "num_errors", "suggest_repair_index", "suggest_prune"}, []string{"broken_packs"})
	if err != nil {
		return err
	}
	var value struct {
		MessageType string   `json:"message_type"`
		Errors      int      `json:"num_errors"`
		Repair      bool     `json:"suggest_repair_index"`
		Prune       bool     `json:"suggest_prune"`
		BrokenPacks []string `json:"broken_packs"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.MessageType != "summary" || value.Errors != 0 || value.Repair || value.Prune || len(value.BrokenPacks) != 0 {
		return ErrIntegrity
	}
	if len(fields) < 4 {
		return ErrIntegrity
	}
	return nil
}
