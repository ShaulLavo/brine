package main

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
)

type fileRead struct {
	data []byte
	err  error
}

type boundedFileInput struct {
	file      *os.File
	closed    chan struct{}
	closeOnce sync.Once
	reader    io.Reader
}

func (r *boundedFileInput) Read(p []byte) (int, error) {
	if r.reader == nil {
		done := make(chan fileRead, 1)
		go func() {
			data, err := io.ReadAll(io.LimitReader(r.file, dispatch.RequestLimit+1))
			done <- fileRead{data, err}
		}()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case result := <-done:
			if result.err != nil {
				return 0, result.err
			}
			r.reader = bytes.NewReader(result.data)
		case <-r.closed:
			return 0, os.ErrClosed
		case <-timer.C:
			return 0, os.ErrDeadlineExceeded
		}
	}
	return r.reader.Read(p)
}

func (r *boundedFileInput) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return r.file.Close()
}
