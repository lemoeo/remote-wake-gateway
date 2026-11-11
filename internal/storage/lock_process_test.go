package storage

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestStoreLockProcessHelper(t *testing.T) {
	dir := os.Getenv("RWG_TEST_LOCK_DIR")
	if dir == "" {
		return
	}
	s, err := Open(dir)
	if err != nil {
		os.Exit(23)
	}
	if os.Getenv("RWG_TEST_LOCK_HOLD") == "1" {
		os.Stdout.WriteString("locked\n")
		time.Sleep(time.Minute)
	}
	s.Close()
	os.Exit(0)
}

func lockCommand(dir string, hold bool) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockProcessHelper$")
	cmd.Env = append(os.Environ(), "RWG_TEST_LOCK_DIR="+dir, "RWG_TEST_LOCK_HOLD=0")
	if hold {
		cmd.Env[len(cmd.Env)-1] = "RWG_TEST_LOCK_HOLD=1"
	}
	return cmd
}

func TestLockAcrossProcessesAndAfterForcedExit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var exit *exec.ExitError
	if err = lockCommand(dir, false).Run(); !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("second process acquired held directory or failed unexpectedly: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatalf("close must be idempotent: %v", err)
	}
	if err = lockCommand(dir, false).Run(); err != nil {
		t.Fatalf("child could not acquire released lock: %v", err)
	}
	cmd := lockCommand(dir, true)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		ready <- scanner.Scan() && scanner.Text() == "locked"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed to acquire lock")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child lock acquisition timed out")
	}
	if second, err := Open(dir); err == nil {
		second.Close()
		t.Fatal("parent acquired child's lock")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reopened, err := Open(dir)
		if err == nil {
			reopened.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released after forced exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
