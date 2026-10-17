package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/enroll"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

type PermissionClient interface {
	Call(context.Context, transport.Target, string, string, enroll.PermissionsRequest) (enroll.PermissionsResponse, error)
}

func newPermissionsCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var target, configDir, admin string
	parent := &cobra.Command{Use: "permissions", Short: "Manage registry, domain and CDN permissions through operator admin SSH"}
	parent.PersistentFlags().StringVar(&target, "target", "", "Pinned enrolled target")
	parent.PersistentFlags().StringVar(&configDir, "config-dir", "", "Private client target directory")
	parent.PersistentFlags().StringVar(&admin, "admin", "", "Operator admin SSH destination; never the restricted deploy credential")
	for _, action := range []string{"show", "plan", "apply", "status"} {
		parent.AddCommand(permissionCommand(deps, modes, action, &target, &configDir, &admin))
	}
	return parent
}

func permissionCommand(deps Dependencies, modes *machineModes, action string, target, configDir, admin *string) *cobra.Command {
	request := enroll.PermissionsRequest{Action: action}
	var allowRegistries, revokeRegistries, allowDomains, revokeDomains []string
	var allowCDNBindings, revokeCDNBindings []string
	cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		changes, err := permissionActions(allowRegistries, revokeRegistries, allowDomains, revokeDomains, allowCDNBindings, revokeCDNBindings)
		if err != nil {
			return err
		}
		request.Changes = changes
		response, err := callPermissions(cmd.Context(), deps, *target, *configDir, *admin, request)
		if err != nil {
			return err
		}
		if modes.enabled() {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), response))
		}
		raw, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}}
	if action == "plan" {
		cmd.Flags().StringArrayVar(&allowRegistries, "allow-registry", nil, "Allow an exact REGISTRY/REPOSITORY prefix (repeatable)")
		cmd.Flags().StringArrayVar(&revokeRegistries, "revoke-registry", nil, "Revoke an exact REGISTRY/REPOSITORY prefix (repeatable)")
		cmd.Flags().StringArrayVar(&allowDomains, "allow-domain", nil, "Allow an exact domain (repeatable)")
		cmd.Flags().StringArrayVar(&revokeDomains, "revoke-domain", nil, "Revoke an exact domain (repeatable)")
		cmd.Flags().StringArrayVar(&allowCDNBindings, "allow-cdn-binding", nil, "Allow the exact typed CDN binding from a JSON file (repeatable)")
		cmd.Flags().StringArrayVar(&revokeCDNBindings, "revoke-cdn-binding", nil, "Revoke the matching exact typed CDN binding from a JSON file (repeatable)")
		cmd.Flags().StringVar(&request.AppLabel, "app", "", "Optional app label for the audit record; does not grant app-specific authority")
	}
	if action == "apply" {
		cmd.Flags().StringVar(&request.PlanID, "plan-id", "", "Reviewed permission plan ID")
	}
	if action == "apply" || action == "status" {
		cmd.Flags().StringVar(&request.IdempotencyKey, "idempotency-key", "", "Stable 32-hex permission operation key")
	}
	return cmd
}

func permissionActions(allowRegistry, revokeRegistry, allowDomain, revokeDomain, allowCDNBinding, revokeCDNBinding []string) ([]policy.PermissionAction, error) {
	var changes []policy.PermissionAction
	for _, group := range []struct {
		kind   policy.PermissionKind
		values []string
	}{{policy.AllowRegistry, allowRegistry}, {policy.RevokeRegistry, revokeRegistry}, {policy.AllowDomain, allowDomain}, {policy.RevokeDomain, revokeDomain}, {policy.AllowCDNBinding, allowCDNBinding}, {policy.RevokeCDNBinding, revokeCDNBinding}} {
		for _, value := range group.values {
			change, err := permissionAction(group.kind, value)
			if err != nil {
				return nil, err
			}
			changes = append(changes, change)
		}
	}
	return changes, nil
}
func permissionAction(kind policy.PermissionKind, value string) (policy.PermissionAction, error) {
	if kind == policy.AllowCDNBinding || kind == policy.RevokeCDNBinding {
		binding, err := permissionCDNBinding(value)
		if err != nil {
			return policy.PermissionAction{}, err
		}
		return policy.PermissionAction{Kind: kind, CDNBinding: &binding}, nil
	}
	if kind == policy.AllowDomain || kind == policy.RevokeDomain {
		return policy.PermissionAction{Kind: kind, Domain: value}, nil
	}
	host, repository, ok := strings.Cut(value, "/")
	if !ok || repository == "" {
		return policy.PermissionAction{}, result.New(result.InvalidUsage, nil)
	}
	return policy.PermissionAction{Kind: kind, Registry: &policy.Registry{Host: host, RepositoryPrefixes: []string{repository}}}, nil
}

func permissionCDNBinding(filename string) (cdn.Binding, error) {
	input, err := os.Open(filename) // #nosec G304 -- Explicit client-selected typed binding, never a host pathname.
	if err != nil {
		return cdn.Binding{}, result.New(result.InvalidUsage, nil)
	}
	defer func() { _ = input.Close() }()
	raw, err := io.ReadAll(io.LimitReader(input, 64<<10+1))
	if err != nil {
		return cdn.Binding{}, result.New(result.InvalidUsage, nil)
	}
	binding, err := cdn.ParseBinding(raw)
	if err != nil {
		return cdn.Binding{}, result.New(result.InvalidUsage, nil)
	}
	return binding, nil
}

func callPermissions(ctx context.Context, deps Dependencies, name, dir, admin string, request enroll.PermissionsRequest) (enroll.PermissionsResponse, error) {
	if !transport.ValidTargetName(name) || admin == "" {
		return enroll.PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return enroll.PermissionsResponse{}, err
		}
		dir = filepath.Join(base, "brine", "targets")
	}
	load := deps.LoadOperationTarget
	if load == nil {
		load = loadOperationTarget
	}
	target, err := load(dir, name)
	if err != nil {
		return enroll.PermissionsResponse{}, err
	}
	client := deps.PermissionClient
	if client == nil {
		client = enroll.PermissionsClient{}
	}
	return client.Call(ctx, target, dir, admin, request)
}

func newHostPermissionsCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{Use: "permissions", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		response, err := executeHostPermissions(cmd.Context(), deps.Stdin, enroll.PermissionsHostOperation)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), response))
	}}
}

func executeHostPermissions(ctx context.Context, input io.Reader, execute func(context.Context, enroll.PermissionsRequest) (enroll.PermissionsResponse, error)) (enroll.PermissionsResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 128<<10+1))
	if err != nil || len(raw) > 128<<10 {
		return enroll.PermissionsResponse{}, result.New(result.InvalidUsage, err)
	}
	defer clear(raw)
	request, err := enroll.DecodePermissionsRequest(raw)
	if err != nil {
		return enroll.PermissionsResponse{}, result.New(result.InvalidUsage, err)
	}
	return execute(ctx, request)
}
