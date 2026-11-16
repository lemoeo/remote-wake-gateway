package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewHelpAliases(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		var output bytes.Buffer
		_, handled, err := parseCommand(args, &output)
		if err != nil || !handled || exitCode(err) != 0 {
			t.Fatalf("help failed for %v: handled=%v err=%v", args, handled, err)
		}
		for _, text := range []string{"RemoteWake Gateway v" + strings.TrimPrefix(version, "v"), "reset-password", "help [命令]", "127.0.0.1:9000", "不指定命令时执行 serve"} {
			if !strings.Contains(output.String(), text) {
				t.Errorf("%v missing %q", args, text)
			}
		}
	}
}

func TestEveryCommandHelpAlias(t *testing.T) {
	for _, info := range commands {
		for _, args := range [][]string{{"help", info.name}, {info.name, "--help"}, {info.name, "-h"}} {
			var output bytes.Buffer
			_, handled, err := parseCommand(args, &output)
			if err != nil || !handled {
				t.Fatalf("%v did not display help: %v", args, err)
			}
			if !strings.Contains(output.String(), "remotewake-gateway "+info.usage) || !strings.Contains(output.String(), info.description) {
				t.Errorf("%v missing command-specific usage: %s", args, output.String())
			}
			if strings.Contains(output.String(), "flag: help requested") {
				t.Errorf("%v printed an internal help error", args)
			}
		}
	}
}

func TestCommandHelpShowsOnlyRelevantFlags(t *testing.T) {
	for _, tc := range []struct {
		command string
		flags   string
	}{
		{"serve", "data-dir listen"},
		{"init", "data-dir password-stdin"},
		{"reset-password", "data-dir password-stdin"},
		{"wake", "device password-stdin url"},
		{"check", "data-dir"},
		{"token", "data-dir"},
		{"interfaces", ""},
		{"version", ""},
	} {
		var output bytes.Buffer
		writeHelp(&output, tc.command)
		for _, name := range []string{"data-dir", "listen", "device", "password-stdin", "url"} {
			found := strings.Contains(output.String(), "\n  --"+name+" ")
			want := strings.Contains(" "+tc.flags+" ", " "+name+" ")
			if found != want {
				t.Errorf("%s help flag --%s: found=%v want=%v", tc.command, name, found, want)
			}
		}
	}
}

func TestCommandDefaultsAndOverrides(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}} {
		o, handled, err := parseCommand(args, io.Discard)
		if err != nil || handled || o.command != "serve" || o.dataDir != "./data" || o.listen != "" {
			t.Fatalf("default service behavior changed: %v %+v %v", args, o, err)
		}
	}
	o, handled, err := parseCommand([]string{"serve", "--data-dir", "data folder", "--listen", "127.0.0.1:9001"}, io.Discard)
	if err != nil || handled || o.dataDir != "data folder" || o.listen != "127.0.0.1:9001" {
		t.Fatalf("service overrides lost: %+v %v", o, err)
	}
	o, handled, err = parseCommand([]string{"wake", "--device", "dev_xxx", "--password-stdin"}, io.Discard)
	if err != nil || handled || o.command != "wake" || o.device != "dev_xxx" || !o.passwordStdin || o.apiURL != "http://127.0.0.1:9000" {
		t.Fatalf("wake options lost: %+v %v", o, err)
	}
	for _, command := range []string{"init", "reset-password", "check", "token"} {
		args := []string{command, "--data-dir", "custom-data"}
		if command == "init" || command == "reset-password" {
			args = append(args, "--password-stdin")
		}
		o, handled, err := parseCommand(args, io.Discard)
		if err != nil || handled || o.command != command || o.dataDir != "custom-data" {
			t.Errorf("%s options lost: %+v %v", command, o, err)
		}
	}
}

func TestInvalidCommandArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		text string
	}{
		{[]string{"unknown"}, "未知命令"},
		{[]string{"help", "unknown"}, "未知命令"},
		{[]string{"help", "wake", "extra"}, "最多接受一个命令"},
		{[]string{"--help", "wake"}, "不接受额外参数"},
		{[]string{"serve", "--device", "dev_xxx"}, "flag provided but not defined"},
		{[]string{"wake", "--data-dir", "./data"}, "flag provided but not defined"},
		{[]string{"version", "--listen", ":9000"}, "flag provided but not defined"},
		{[]string{"interfaces", "--url", "http://localhost:9000"}, "flag provided but not defined"},
		{[]string{"serve", "--listen"}, "flag needs an argument"},
		{[]string{"serve", "--data-dir="}, "--data-dir 不能为空"},
		{[]string{"check", "extra"}, "不接受位置参数"},
		{[]string{"wake"}, "缺少必填参数 --device"},
		{[]string{"wake", "--device", "bad/id", "--password-stdin"}, "不是有效的设备 ID"},
		{[]string{"wake", "--device", "dev_xxx"}, "缺少必填参数 --password-stdin"},
		{[]string{"init"}, "需要提供 --password-stdin"},
		{[]string{"reset-password"}, "需要提供 --password-stdin"},
	} {
		var output bytes.Buffer
		_, handled, err := parseCommand(tc.args, &output)
		if err == nil || handled || exitCode(err) != 2 || !strings.Contains(err.Error(), tc.text) || !strings.Contains(err.Error(), "用法：") || output.Len() != 0 {
			t.Errorf("unexpected argument error for %v: handled=%v stdout=%q err=%v", tc.args, handled, output.String(), err)
		}
	}
	if exitCode(errors.New("runtime failure")) != 1 || exitCode(fmt.Errorf("wrapped: %w", &usageError{message: "bad arguments"})) != 2 {
		t.Fatal("exit code classification failed")
	}
}

func TestCLIProcess(t *testing.T) {
	if os.Getenv("RWG_CLI_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"remotewake-gateway"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(99)
}

func TestCLIHelpExitCodesAndNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"help"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"-h"}, 0},
		{[]string{"help", "wake"}, 0},
		{[]string{"serve", "--data-dir", "untouched", "--listen", ":0", "--help"}, 0},
		{[]string{"init", "--data-dir", "untouched", "--password-stdin", "--help"}, 0},
		{[]string{"reset-password", "--data-dir", "untouched", "-h"}, 0},
		{[]string{"wake", "--url", "http://127.0.0.1:1", "--help"}, 0},
		{[]string{"version", "--help"}, 0},
		{[]string{"unknown"}, 2},
		{[]string{"serve", "--device", "dev_xxx"}, 2},
		{[]string{"init", "--data-dir", "untouched"}, 2},
		{[]string{"reset-password", "--data-dir", "untouched"}, 2},
		{[]string{"wake", "--device", "dev_xxx"}, 2},
		{[]string{"token", "--data-dir", "missing"}, 1},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"-test.run=^TestCLIProcess$", "--"}, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "RWG_CLI_HELPER=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var status *exec.ExitError
				if !errors.As(err, &status) {
					t.Fatal(err)
				}
				code = status.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, tc.code, stdout.String(), stderr.String())
			}
			if code == 0 && (stdout.Len() == 0 || stderr.Len() != 0) || code != 0 && (stdout.Len() != 0 || stderr.Len() == 0) {
				t.Fatalf("incorrect output streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			for _, name := range []string{"data", "untouched", "missing"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("CLI unexpectedly created %s: %v", name, err)
				}
			}
		})
	}
}
