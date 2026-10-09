//go:build !unix

package main

import (
	"errors"
	"io"
)

func hostInput() io.ReadCloser {
	return failedInput{errors.New("host dispatcher requires Unix input deadlines")}
}
