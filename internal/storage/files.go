package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const MaxFile = 4 << 20

type Store struct {
	Dir       string
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"", "secrets", "jobs"} {
		if err = os.MkdirAll(filepath.Join(abs, sub), 0700); err != nil {
			return nil, err
		}
		if err = protectDirectory(filepath.Join(abs, sub)); err != nil {
			return nil, fmt.Errorf("无法保护数据目录权限: %w", err)
		}
	}
	f, err := os.OpenFile(filepath.Join(abs, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("数据目录已被占用或无法锁定: %w", err)
	}
	if err = protectFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("无法保护锁文件权限: %w", err)
	}
	return &Store{Dir: abs, lock: f}, nil
}
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = errors.Join(unlockFile(s.lock), s.lock.Close()) })
	return s.closeErr
}
func (s *Store) Path(name string) string { return filepath.Join(s.Dir, name) }
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("只允许一个 JSON 值")
	}
	return nil
}
func (s *Store) Read(name string, v any) error {
	f, err := os.Open(s.Path(name))
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return err
	}
	if len(b) > MaxFile {
		return errors.New("数据文件过大")
	}
	if err = Decode(b, v); err != nil {
		return fmt.Errorf("读取 %s 失败: %w", name, err)
	}
	return nil
}
func (s *Store) Write(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > MaxFile {
		return errors.New("数据文件过大")
	}
	return s.WriteBytes(name, append(b, '\n'))
}

// Contents are flushed before publishing a replacement. Platform-specific
// publication handles directory sync on Unix and write-through moves on Windows.
func (s *Store) WriteBytes(name string, b []byte) error {
	dest := s.Path(name)
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = protectFile(f); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, dest)
}
