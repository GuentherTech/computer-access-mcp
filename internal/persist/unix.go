//go:build !windows

package persist

import (
	"golang.org/x/sys/unix"
	"os"
)

func seal(data []byte) ([]byte, error)   { return data, nil }
func unseal(data []byte) ([]byte, error) { return data, nil }
func lockFile(f *os.File) error          { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
