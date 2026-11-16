package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

type commandOptions struct {
	command       string
	dataDir       string
	listen        string
	device        string
	passwordStdin bool
	apiURL        string
}

type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func invalidUsage(command, message string) error {
	var output bytes.Buffer
	fmt.Fprintf(&output, "错误：%s\n\n", message)
	writeHelp(&output, command)
	return &usageError{message: strings.TrimSpace(output.String())}
}

func commandFlags(command string, options *commandOptions) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// Render help and errors once, through the CLI's own output paths.
	flags.SetOutput(io.Discard)
	switch command {
	case "serve", "init", "reset-password", "check", "token":
		flags.StringVar(&options.dataDir, "data-dir", "./data", "数据目录，相对于当前工作目录")
	}
	switch command {
	case "serve":
		flags.StringVar(&options.listen, "listen", "", "覆盖本次启动的监听地址，未指定时读取配置")
	case "init", "reset-password":
		flags.BoolVar(&options.passwordStdin, "password-stdin", false, "从标准输入读取新管理员密码，必填")
	case "wake":
		flags.StringVar(&options.device, "device", "", "已保存设备的 ID，必填")
		flags.BoolVar(&options.passwordStdin, "password-stdin", false, "从标准输入读取管理员登录密码，必填")
		flags.StringVar(&options.apiURL, "url", "http://127.0.0.1:9000", "正在运行的 Gateway 地址")
	}
	return flags
}

func parseCommand(args []string, output io.Writer) (options commandOptions, handled bool, err error) {
	options.command = "serve"
	if len(args) == 0 {
		commandFlags(options.command, &options)
		return options, false, nil
	}
	command := args[0]
	if command == "-h" || command == "--help" {
		if len(args) != 1 {
			return options, false, invalidUsage("", "总览帮助不接受额外参数；请使用 help <命令>")
		}
		writeHelp(output, "")
		return options, true, nil
	}
	if command == "help" {
		if len(args) > 2 {
			return options, false, invalidUsage("help", "help 最多接受一个命令名称")
		}
		target := ""
		if len(args) == 2 {
			target = args[1]
			if target == "-h" || target == "--help" {
				target = "help"
			}
			if findCommand(target) == nil {
				return options, false, invalidUsage("", fmt.Sprintf("未知命令 %q", target))
			}
		}
		writeHelp(output, target)
		return options, true, nil
	}
	if findCommand(command) == nil {
		return options, false, invalidUsage("", fmt.Sprintf("未知命令 %q", command))
	}
	options.command = command
	flags := commandFlags(command, &options)
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		writeHelp(output, command)
		return options, true, nil
	} else if err != nil {
		return options, false, invalidUsage(command, err.Error())
	}
	if flags.NArg() != 0 {
		return options, false, invalidUsage(command, fmt.Sprintf("不接受位置参数：%s", strings.Join(flags.Args(), " ")))
	}
	if options.dataDir == "" && flags.Lookup("data-dir") != nil {
		return options, false, invalidUsage(command, "--data-dir 不能为空")
	}
	switch command {
	case "init", "reset-password":
		if !options.passwordStdin {
			return options, false, invalidUsage(command, "需要提供 --password-stdin；请先停止使用该数据目录的服务")
		}
	case "wake":
		if options.device == "" {
			return options, false, invalidUsage(command, "缺少必填参数 --device ID")
		}
		if !model.ValidID(options.device) {
			return options, false, invalidUsage(command, "--device 不是有效的设备 ID")
		}
		if !options.passwordStdin {
			return options, false, invalidUsage(command, "缺少必填参数 --password-stdin")
		}
	}
	return options, false, nil
}
