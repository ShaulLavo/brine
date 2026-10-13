//go:build !linux

package runnerstorage

func CheckLocal() error { return ErrProfile }
