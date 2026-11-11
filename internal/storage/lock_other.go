//go:build !linux && !darwin && !freebsd && !windows

package storage

import (
	"errors"
	"os"
)

func lockFile(f *os.File) error {
	return errors.New("当前版本仅支持 Linux、macOS、FreeBSD 和 Windows 文件锁")
}
func unlockFile(f *os.File) error { return nil }
