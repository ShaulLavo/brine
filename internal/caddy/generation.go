package caddy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShaulLavo/brine/internal/spec"
)

const (
	maxFileBytes   = 1 << 20
	maxSetBytes    = 16 << 20
	commandTimeout = 10 * time.Second
)

// Validator runs caddy validate --adapter caddyfile --config candidate.
// Implementations must use bounded subprocess output and honor the context.
type Validator interface {
	Validate(ctx context.Context, candidate string) error
}

// Reloader runs only systemctl reload caddy.service, never reload-or-restart.
type Reloader interface {
	Reload(ctx context.Context) error
}

type Outcome string

const (
	Unchanged        Outcome = "unchanged"
	Applied          Outcome = "applied"
	RolledBack       Outcome = "rolled_back"
	Unknown          Outcome = "unknown_outcome"
	RecoveryRequired Outcome = "recovery_required"
)

// State is supplied from committed control state, not invented from live files.
// Files maps app filenames (including .caddy) to sha256:<lowercase hex>.
type State struct {
	Generation uint64
	Files      map[string]string
}

type Result struct {
	Outcome  Outcome
	Previous uint64
	Current  uint64
	Next     State
	Stage    string
}

type OperationError struct {
	Stage string
	Cause error
}

func (e *OperationError) Error() string { return "caddy: operation failed at " + e.Stage }
func (e *OperationError) Unwrap() error { return e.Cause }

type UnknownOutcomeError struct {
	Stage string
	Cause error
}

func (e *UnknownOutcomeError) Error() string {
	return "caddy: reload outcome unknown; reconciliation required"
}
func (e *UnknownOutcomeError) Unwrap() error { return e.Cause }

type Change struct {
	name spec.Name
	site Site
}

func Put(site Site) Change         { return Change{name: site.name, site: site} }
func Remove(name spec.Name) Change { return Change{name: name} }

var (
	appFile        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.caddy$`)
	generationName = regexp.MustCompile(`^gen-(0|[1-9][0-9]*)$`)
	safeRoot       = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)
)

// Manager confines all filesystem operations to an existing owned directory.
// The caller must hold the host mutation lock through Observe/Apply and commit.
// The mutex only serializes calls on this handle, not independent processes.
type Manager struct {
	root       *os.Root
	path       string
	validator  Validator
	reloader   Reloader
	mu         sync.Mutex
	stopped    bool
	checkpoint func(string) error
}

func NewManager(root string, validator Validator, reloader Reloader) (*Manager, error) {
	if root == "/" || filepath.Clean(root) != root || !safeRoot.MatchString(root) || validator == nil || reloader == nil {
		return nil, errors.New("caddy: owned root and adapters required")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, errors.New("caddy: owned root must not contain symlinks")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, &OperationError{Stage: "open", Cause: err}
	}
	return &Manager{root: r, path: root, validator: validator, reloader: reloader}, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	return m.root.Close()
}

// Observe describes disk only. It does not prove which generation Caddy serves.
func (m *Manager) Observe() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return State{}, errors.New("caddy: manager closed or requires reconciliation")
	}
	state, _, err := m.observe()
	return state, err
}
func (m *Manager) CheckDrift(expected State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return errors.New("caddy: manager closed or requires reconciliation")
	}
	observed, _, err := m.observe()
	if err != nil {
		return err
	}
	return compareState(observed, expected)
}
func compareState(observed, expected State) error {
	if expected.Files == nil || observed.Generation != expected.Generation || !maps.Equal(observed.Files, expected.Files) {
		return errors.New("caddy: generation drift")
	}
	return nil
}

func (m *Manager) observe() (State, map[string][]byte, error) {
	target, err := m.root.Readlink("current")
	if err != nil {
		return State{}, nil, err
	}
	if filepath.IsAbs(target) {
		if filepath.Dir(target) != m.path {
			return State{}, nil, errors.New("caddy: current escapes owned root")
		}
		target = filepath.Base(target)
	}
	n, err := generationNumber(target)
	if err != nil {
		return State{}, nil, err
	}
	files, err := m.readGeneration(target)
	if err != nil {
		return State{}, nil, err
	}
	return State{Generation: n, Files: hashFiles(files)}, files, nil
}
func generationNumber(name string) (uint64, error) {
	if !generationName.MatchString(name) {
		return 0, errors.New("caddy: invalid generation")
	}
	return strconv.ParseUint(strings.TrimPrefix(name, "gen-"), 10, 64)
}
func gen(n uint64) string { return "gen-" + strconv.FormatUint(n, 10) }
func hashFiles(files map[string][]byte) map[string]string {
	hashes := make(map[string]string, len(files))
	for name, content := range files {
		hashes[name] = fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	}
	return hashes
}
func (m *Manager) readGeneration(name string) (map[string][]byte, error) {
	info, err := m.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("caddy: generation is not a directory")
	}
	dir, err := m.root.Open(name)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > 4096 {
		return nil, errors.New("caddy: too many files")
	}
	files := make(map[string][]byte, len(entries))
	total := 0
	for _, entry := range entries {
		if !appFile.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			return nil, errors.New("caddy: unexpected generation entry")
		}
		path := name + "/" + entry.Name()
		info, err := m.root.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			return nil, errors.New("caddy: unsafe generation file")
		}
		f, err := m.root.Open(path)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		total += len(b)
		if len(b) > maxFileBytes || total > maxSetBytes {
			return nil, errors.New("caddy: generation exceeds size limit")
		}
		files[entry.Name()] = b
	}
	return files, nil
}

func candidateRoot(main []byte, root, next string) ([]byte, error) {
	if len(main) == 0 || len(main) > maxFileBytes {
		return nil, errors.New("caddy: root config exceeds size limit")
	}
	// Caddy expands environment variables before tokenizing. Such expansion
	// would invalidate source spans and can introduce imports absent from disk.
	if bytes.Contains(main, []byte("{$")) {
		return nil, errors.New("caddy: root environment substitution is unsupported")
	}
	tokens, err := rootTokens(main)
	if err != nil {
		return nil, err
	}
	wanted := root + "/current/*.caddy"
	found, depth := -1, 0
	for i, token := range tokens {
		if token.quote != 0 && (token.text == "{" || token.text == "}" || token.text == "{}") {
			return nil, errors.New("caddy: ambiguous quoted root brace")
		}
		lineStart := i == 0 || tokens[i-1].line+strings.Count(tokens[i-1].text, "\n") < token.line
		directiveStart := lineStart || (depth > 0 && i > 0 && tokens[i-1].text == "{" && tokens[i-1].quote == 0)
		if token.text == "import" && directiveStart {
			if i+1 == len(tokens) || tokens[i+1].line != token.line {
				return nil, errors.New("caddy: import argument missing")
			}
			argument := tokens[i+1]
			// Relocation changes Caddy's relative file-import base. Only actual
			// directives are checked; response values named import remain untouched.
			if !strings.HasPrefix(argument.text, "/") {
				return nil, errors.New("caddy: root config must use absolute imports")
			}
			if argument.text == wanted {
				if depth != 0 || !lineStart || found != -1 {
					return nil, errors.New("caddy: Brine import must be unique and top-level")
				}
				if i+2 < len(tokens) && tokens[i+2].line == argument.line+strings.Count(argument.text, "\n") {
					return nil, errors.New("caddy: Brine import must have exactly one argument")
				}
				found = i + 1
			}
		}
		if token.quote == 0 && token.text == "{" {
			depth++
		}
		if token.quote == 0 && token.text == "}" {
			depth--
			if depth < 0 {
				return nil, errors.New("caddy: unmatched root brace")
			}
		}
	}
	if found == -1 || depth != 0 {
		return nil, errors.New("caddy: top-level Brine import missing or root braces unmatched")
	}
	argument := tokens[found]
	replacement := root + "/" + next + "/*.caddy"
	if argument.quote != 0 {
		replacement = string(argument.quote) + replacement + string(argument.quote)
	}
	candidate := make([]byte, 0, len(main)+len(replacement))
	candidate = append(candidate, main[:argument.start]...)
	candidate = append(candidate, replacement...)
	candidate = append(candidate, main[argument.end:]...)
	return candidate, nil
}

func (m *Manager) nextGeneration(current uint64) (uint64, error) {
	dir, err := m.root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	highest := current
	for _, entry := range entries {
		if !generationName.MatchString(entry.Name()) {
			continue
		}
		n, err := generationNumber(entry.Name())
		if err != nil {
			return 0, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return 0, errors.New("caddy: unsafe generation entry")
		}
		if n > highest {
			highest = n
		}
	}
	if highest == ^uint64(0) {
		return 0, errors.New("caddy: generation exhausted")
	}
	return highest + 1, nil
}
func (m *Manager) write(path string, content []byte) error {
	f, err := m.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(content)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func (m *Manager) syncDir(path string) error {
	dir, err := m.root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (m *Manager) point(target, temporary string, result *Result, prefix string) error {
	if err := m.root.Symlink(target, temporary); err != nil {
		return err
	}
	if err := m.step(result, prefix+"link-created"); err != nil {
		return err
	}
	if err := m.root.Rename(temporary, "current"); err != nil {
		return err
	}
	n, _ := generationNumber(target)
	result.Current = n
	result.Outcome = RecoveryRequired
	if err := m.step(result, prefix+"current-renamed"); err != nil {
		return err
	}
	if err := m.syncDir("."); err != nil {
		return err
	}
	return m.step(result, prefix+"current-synced")
}
func (m *Manager) step(result *Result, name string) error {
	result.Stage = name
	if m.checkpoint != nil {
		return m.checkpoint(name)
	}
	return nil
}
func (m *Manager) reload(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	err := m.reloader.Reload(ctx)
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	return err
}
func unknown(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &timeout) && timeout.Timeout())
}

func (m *Manager) Apply(ctx context.Context, main []byte, expected State, change Change) (result Result, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result = Result{Outcome: Unchanged, Previous: expected.Generation, Current: expected.Generation, Stage: "preflight"}
	defer func() {
		if err != nil {
			var outcome *UnknownOutcomeError
			if !errors.As(err, &outcome) {
				err = &OperationError{Stage: result.Stage, Cause: err}
			}
			if result.Outcome == RecoveryRequired || result.Outcome == Unknown {
				m.stopped = true
			}
		}
	}()
	if m.stopped {
		return result, errors.New("caddy: manager closed or requires reconciliation")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if !appFile.MatchString(string(change.name) + ".caddy") {
		return result, errors.New("caddy: invalid app change")
	}
	observed, files, err := m.observe()
	if err != nil {
		return result, err
	}
	result.Current = observed.Generation
	if err = compareState(observed, expected); err != nil {
		return result, err
	}
	next, err := m.nextGeneration(observed.Generation)
	if err != nil {
		return result, err
	}
	name := gen(next)
	candidate, err := candidateRoot(main, m.path, name)
	if err != nil {
		return result, err
	}
	app := string(change.name) + ".caddy"
	if change.site.name == "" {
		if _, exists := files[app]; !exists {
			return result, errors.New("caddy: removal requires owned app file")
		}
		delete(files, app)
	} else {
		content, err := Render(change.site)
		if err != nil {
			return result, err
		}
		files[app] = content
	}
	total := 0
	for _, content := range files {
		total += len(content)
		if len(content) > maxFileBytes || total > maxSetBytes || len(files) > 4096 {
			return result, errors.New("caddy: next generation exceeds size limit")
		}
	}
	result.Next = State{Generation: next, Files: hashFiles(files)}
	if err = m.root.Mkdir(name, 0755); err != nil {
		return result, err
	}
	if err = m.step(&result, "generation-created"); err != nil {
		return result, err
	}
	names := slices.Sorted(maps.Keys(files))
	for _, file := range names {
		if err = m.write(name+"/"+file, files[file]); err != nil {
			return result, err
		}
		if err = m.step(&result, "file-written"); err != nil {
			return result, err
		}
	}
	if err = m.syncDir(name); err != nil {
		return result, err
	}
	if err = m.syncDir("."); err != nil {
		return result, err
	}
	if err = m.step(&result, "generation-synced"); err != nil {
		return result, err
	}
	candidateName := "candidate-" + name + ".caddy"
	if err = m.write(candidateName, candidate); err != nil {
		return result, err
	}
	if err = m.syncDir("."); err != nil {
		return result, err
	}
	if err = m.step(&result, "candidate-written"); err != nil {
		return result, err
	}
	result.Stage = "validate"
	validateCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	err = m.validator.Validate(validateCtx, filepath.Join(m.path, candidateName))
	if validateCtx.Err() != nil {
		err = errors.Join(err, validateCtx.Err())
	}
	cancel()
	if err != nil {
		return result, err
	}
	if err = m.step(&result, "validated"); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = m.point(name, ".current-"+name, &result, ""); err != nil {
		return result, err
	}
	result.Stage = "reload"
	if err = m.reload(ctx); err != nil {
		if unknown(err) {
			result.Outcome = Unknown
			return result, &UnknownOutcomeError{Stage: result.Stage, Cause: err}
		}
		reloadErr := err
		if err = m.point(gen(observed.Generation), ".rollback-"+name, &result, "rollback-"); err != nil {
			return result, errors.Join(reloadErr, err)
		}
		result.Stage = "rollback-reload"
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
		err = m.reload(rollbackCtx)
		cancel()
		if unknown(err) {
			result.Outcome = Unknown
			return result, &UnknownOutcomeError{Stage: result.Stage, Cause: errors.Join(reloadErr, err)}
		}
		if err != nil {
			return result, errors.Join(reloadErr, err)
		}
		result.Outcome = RolledBack
		return result, reloadErr
	}
	result.Outcome = Applied
	if err = m.step(&result, "reloaded"); err != nil {
		return result, err
	}
	if err = m.prune(observed.Generation, next); err != nil {
		return result, err
	}
	err = m.step(&result, "pruned")
	return result, err
}

func (m *Manager) prune(previous, current uint64) error {
	dir, err := m.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		n, err := generationNumber(entry.Name())
		if err != nil || n == previous || n == current || n > current {
			continue
		}
		files, err := m.readGeneration(entry.Name())
		if err != nil {
			return err
		}
		for _, file := range slices.Sorted(maps.Keys(files)) {
			if err := m.root.Remove(entry.Name() + "/" + file); err != nil {
				return err
			}
		}
		if err := m.root.Remove(entry.Name()); err != nil {
			return err
		}
		candidate := "candidate-" + entry.Name() + ".caddy"
		if info, err := m.root.Lstat(candidate); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("caddy: unsafe candidate entry")
			}
			if err := m.root.Remove(candidate); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return m.syncDir(".")
}
