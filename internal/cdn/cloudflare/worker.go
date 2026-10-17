package cloudflare

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
)

//go:embed worker.mjs
var workerTemplate string

type workerPolicy struct {
	Hostname   string   `json:"hostname"`
	Prefix     string   `json:"prefix"`
	Extensions []string `json:"extensions"`
	Namespace  string   `json:"namespace"`
	TTLSeconds uint32   `json:"ttlSeconds"`
}

func WorkerSource(attachment cdn.Attachment) ([]byte, error) {
	if attachment.Binding.Validate() != nil || attachment.Owner.Attachment == "" {
		return nil, cdn.ErrInvalid
	}
	extensions := slices.Clone(attachment.Binding.Cache.Extensions)
	slices.Sort(extensions)
	cachePolicy := attachment.Binding.Cache
	cachePolicy.Extensions = extensions
	identity, err := json.Marshal(struct {
		Owner  cdn.Owner
		Policy cdn.CachePolicy
	}{attachment.Owner, cachePolicy})
	if err != nil {
		return nil, cdn.ErrInvalid
	}
	policy := workerPolicy{Hostname: attachment.Binding.Hostname, Prefix: attachment.Binding.Cache.Prefix,
		Extensions: extensions, Namespace: "brine-cdn-" + strings.TrimPrefix(string(cdn.Hash(identity)), "sha256:"), TTLSeconds: attachment.Binding.Cache.TTLSeconds}
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, cdn.ErrInvalid
	}
	return []byte(strings.Replace(workerTemplate, "/*BRINE_POLICY*/null", string(raw), 1)), nil
}
