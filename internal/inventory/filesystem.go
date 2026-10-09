package inventory

import (
	"context"
	"time"
)

// Kernel filesystem calls are not always interruptible (for example, a stuck
// mount). Bound both the caller's wait and the outstanding worker count.
var filesystemSlots = make(chan struct{}, 8)

type filesystemResult[T any] struct {
	value T
	err   error
}

func filesystemCall[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if e := ctx.Err(); e != nil {
		return zero, e
	}
	select {
	case filesystemSlots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	result := make(chan filesystemResult[T], 1)
	go func() { defer func() { <-filesystemSlots }(); v, e := fn(ctx); result <- filesystemResult[T]{v, e} }()
	select {
	case r := <-result:
		if e := ctx.Err(); e != nil {
			return zero, e
		}
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
