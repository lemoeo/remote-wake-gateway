package main

import (
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"os"
	"testing"
)

func TestRecoveryCommandRequiresStoppedService(t *testing.T) {
	dir := t.TempDir()
	fs, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	a, e := auth.New(fs)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Setup(a.Token(), "old-admin-password"); e != nil {
		t.Fatal(e)
	}
	args := []string{"reset-password", "--data-dir", dir, "--password-stdin"}
	if e = run(args); e == nil {
		t.Fatal("recovery acquired an active data directory")
	}
	fs.Close()
	input, e := os.CreateTemp(t.TempDir(), "password")
	if e != nil {
		t.Fatal(e)
	}
	defer input.Close()
	input.WriteString("new123\n")
	input.Seek(0, 0)
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()
	if e = run(args); e != nil {
		t.Fatal(e)
	}
	fs, e = storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	a, e = auth.New(fs)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = a.Login("new123"); e != nil {
		t.Fatal("CLI did not persist new password")
	}
}
