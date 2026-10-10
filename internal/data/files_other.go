//go:build !linux && !darwin

package data

import "os"

func verifyPrivateFile(string) (os.FileInfo, error) { return nil, ErrInvalid }
func verifyPrivateTree(string, string) error        { return ErrInvalid }

func verifyRootAncestors(string) error { return ErrInvalid }

func directoryIdentity(string) (uint64, uint64, error) { return 0, 0, ErrInvalid }
