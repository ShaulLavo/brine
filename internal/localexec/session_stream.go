package localexec

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
)

const SessionStreamOutputLimit = 1 << 20

// StreamExecutor is an explicit capability. Session never substitutes a
// captured Execute call for a streaming request.
type StreamExecutor interface {
	RunStreams(context.Context, Command, io.Reader, io.Writer) error
}

func (s Session) RunStreams(ctx context.Context, path string, args []string, input io.Reader, output io.Writer, outputLimit int, mutation bool) error {
	stream, ok := s.executor.(StreamExecutor)
	if !ok || s.timeout <= 0 || output == nil || outputLimit <= 0 || outputLimit > SessionStreamOutputLimit {
		return &Error{Kind: Invalid}
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if ctx.Err() != nil {
		return &Error{Kind: Timeout}
	}
	environment, err := s.streamEnvironment()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return &Error{Kind: Timeout}
	}
	bounded := &sessionStreamOutput{writer: output, remaining: outputLimit}
	command := Command{Path: path, Args: slices.Clone(args), Env: environment, Dir: s.dir, Timeout: s.timeout, OutputLimit: outputLimit, Mutation: mutation}
	err = stream.RunStreams(ctx, command, input, bounded)
	if bounded.err != nil {
		return &Error{Kind: streamFailureKind(mutation), cause: ErrOutputLimit}
	}
	if ctx.Err() != nil {
		return &Error{Kind: streamFailureKind(mutation)}
	}
	if err == nil {
		return nil
	}
	var runtimeError *Error
	if errors.As(err, &runtimeError) {
		return runtimeError
	}
	kind := Failed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = streamFailureKind(mutation)
	}
	return &Error{Kind: kind}
}

func (s Session) streamEnvironment() ([]string, error) {
	environment, _, err := commandEnvironment(s.env, false)
	if err != nil {
		return nil, err
	}
	// Session's fixed overrides replace the same base keys. RunStreams requires
	// one value per key, so preserve Execute's last-value semantics explicitly.
	seen := make(map[string]bool, len(environment))
	unique := make([]string, 0, len(environment))
	for i := len(environment) - 1; i >= 0; i-- {
		key, _, _ := strings.Cut(environment[i], "=")
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, environment[i])
	}
	slices.Reverse(unique)
	return unique, nil
}

type sessionStreamOutput struct {
	writer    io.Writer
	remaining int
	err       error
}

func (w *sessionStreamOutput) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(p) > w.remaining {
		w.err = ErrOutputLimit
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if n < 0 || n > len(p) {
		w.err = ErrOutputLimit
		return 0, w.err
	}
	w.remaining -= n
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
