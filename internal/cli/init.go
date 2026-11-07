package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/ui"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

type initInput struct {
	Directory, Name, Image, HealthPath string
	ContainerPort                      int
	Domains                            []string
}

type initPreview struct {
	Directory string
	Content   []byte
	App       spec.App
}

type initDocument struct {
	SchemaVersion int        `toml:"schema_version"`
	Name          string     `toml:"name"`
	Image         string     `toml:"image"`
	ContainerPort int        `toml:"container_port"`
	Domains       []string   `toml:"domains"`
	Health        initHealth `toml:"health"`
}

type initHealth struct {
	Path                   string `toml:"path"`
	ExpectedStatus         int    `toml:"expected_status"`
	StartupDeadlineSeconds int    `toml:"startup_deadline_seconds"`
	TimeoutSeconds         int    `toml:"timeout_seconds"`
}

type initResult struct {
	Status       string   `json:"status"`
	File         string   `json:"file"`
	Content      string   `json:"content"`
	Target       string   `json:"target"`
	NextCommands []string `json:"next_commands"`
}

func prepareInit(input initInput) (initPreview, error) {
	if input.Name == "" || !strings.Contains(input.Image, "@sha256:") || input.ContainerPort == 0 || len(input.Domains) == 0 || input.Directory == "" || ui.Sanitize(input.Directory) != input.Directory {
		return initPreview{}, result.New(result.InvalidUsage, nil)
	}
	document := initDocument{1, input.Name, input.Image, input.ContainerPort, input.Domains, initHealth{input.HealthPath, 200, 30, 3}}
	content, err := toml.Marshal(document)
	if err != nil || len(content) > dispatch.RequestLimit/2 {
		return initPreview{}, result.New(result.InvalidUsage, err)
	}
	app, err := spec.Parse(content)
	if err != nil {
		return initPreview{}, result.New(result.InvalidUsage, err)
	}
	document.Image = string(app.Image)
	document.Domains = make([]string, len(app.Domains))
	for i, domain := range app.Domains {
		document.Domains[i] = string(domain)
	}
	content, err = toml.Marshal(document)
	if err != nil {
		return initPreview{}, err
	}
	return initPreview{Directory: input.Directory, Content: content, App: app}, nil
}

func newInitCmd(deps Dependencies, modes *machineModes, noInput *bool) *cobra.Command {
	input := initInput{HealthPath: "/"}
	var flags operationFlags
	var yes bool
	cmd := &cobra.Command{
		Use:   "init [DIRECTORY] --name NAME --image REPOSITORY@sha256:DIGEST --container-port PORT --domain DOMAIN",
		Short: "Preview or create one local brine.toml without host changes",
		Long:  "Generate a local app spec from explicit flags in an existing directory. Machine, no-input and nonterminal invocations preview without writing unless --yes is supplied. Human terminals review with a, then y. Existing identical files are unchanged; other paths are never overwritten. A target checks only its existing local pin. No image, host policy or deployment is verified.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input.Directory = "."
			if len(args) == 1 {
				input.Directory = args[0]
			}
			if !validTUISelection(flags, cmd.Flags().Changed("target")) {
				return result.New(result.InvalidUsage, nil)
			}
			preview, err := prepareInit(input)
			if err != nil {
				return err
			}
			return runInit(cmd, deps, *modes, *noInput, yes, flags, preview)
		},
	}
	cmd.Flags().StringVar(&input.Name, "name", "", "Explicit app name")
	cmd.Flags().StringVar(&input.Image, "image", "", "Explicit OCI image pinned by sha256 digest")
	cmd.Flags().IntVar(&input.ContainerPort, "container-port", 0, "Explicit container HTTP port")
	cmd.Flags().StringArrayVar(&input.Domains, "domain", nil, "Explicit domain, repeat for each domain")
	cmd.Flags().StringVar(&input.HealthPath, "health-path", "/", "HTTP health path")
	cmd.Flags().BoolVar(&yes, "yes", false, "Authorize only local brine.toml creation")
	flags.register(cmd)
	return cmd
}

func runInit(cmd *cobra.Command, deps Dependencies, modes machineModes, noInput, yes bool, flags operationFlags, preview initPreview) (err error) {
	if flags.target != "" {
		if _, err := flags.loadTarget(deps); err != nil {
			return err
		}
	}
	directory, err := openInitDirectory(preview.Directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	status, err := directory.existing(preview.Content)
	if err != nil {
		return err
	}
	if status == "unchanged" {
		return printInit(cmd, modes, flags, preview, status)
	}
	status = "preview"
	switch {
	case yes:
		status, err = directory.create(cmd.Context(), preview.Content, defaultInitFileOps())
	case !modes.enabled() && !noInput && isTerminal(deps.Stdin) && isTerminal(deps.Stdout):
		status, err = approveInit(cmd, deps, directory, preview)
	}
	if err != nil {
		return err
	}
	return printInit(cmd, modes, flags, preview, status)
}

func approveInit(cmd *cobra.Command, deps Dependencies, directory *initDirectory, preview initPreview) (string, error) {
	approved := false
	err := RequestInput(cmd, func(ctx context.Context, input io.Reader, _ io.Writer) error {
		final, err := deps.RunTUI(ctx, newInitModel(preview, humanTheme(deps.Stdout)), input, deps.Stdout)
		model, ok := final.(initModel)
		if err != nil {
			return err
		}
		if !ok {
			return result.New(result.InternalError, nil)
		}
		if model.phase == initInterrupted {
			return context.Canceled
		}
		approved = model.phase == initApproved
		return nil
	})
	if err != nil {
		return "", err
	}
	if !approved {
		return "cancelled", nil
	}
	return directory.create(cmd.Context(), preview.Content, defaultInitFileOps())
}

func printInit(cmd *cobra.Command, modes machineModes, flags operationFlags, preview initPreview, status string) error {
	file := filepath.Join(preview.Directory, "brine.toml")
	target := flags.target
	if target == "" {
		target = "NAME"
	}
	commandFile := file
	if strings.HasPrefix(commandFile, "-") {
		commandFile = "." + string(filepath.Separator) + commandFile
	}
	selection := operationFlags{target: target, configDir: flags.configDir}
	next := selection.command("plan", commandFile, "--json", "--no-input")
	data := initResult{status, file, string(preview.Content), flags.target, []string{next}}
	if modes.enabled() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Success(cmd.CommandPath(), data))
	}
	review := selection.command("tui", "--spec", commandFile)
	_, err := fmt.Fprintf(finalOutputWriter{cmd.OutOrStdout()}, "%s %s\nNo host changes. Image availability and host policy are not checked.\n\n%s\nNext: %s\nMachine plan: %s\n", status, ui.Sanitize(file), preview.Content, ui.Sanitize(review), ui.Sanitize(next))
	return err
}
