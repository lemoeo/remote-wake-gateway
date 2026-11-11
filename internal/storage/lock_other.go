//go:build !linux && !darwin && !freebsd

package storage

import (
	"errors"
	"os"
)

func lockFile(f *os.File) error {
	return errors.New("当前版本仅支持 Linux、macOS 和 FreeBSD 文件锁")
}
