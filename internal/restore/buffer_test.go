package restore

import "sync"

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := len(data)
	remaining := (64 << 10) - len(b.data)
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return size, nil
}
func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data, b.truncated
}
