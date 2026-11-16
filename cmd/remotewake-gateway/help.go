package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type commandHelp struct {
	name        string
	description string
	usage       string
	notes       []string
	examples    []string
}

var commands = []commandHelp{
	{
		name: "serve", description: "启动 Web 管理", usage: "serve [--data-dir PATH] [--listen HOST:PORT]",
		notes: []string{
			"未指定 --listen 时读取 config.json；首次启动默认监听 127.0.0.1:9000。",
			"--listen 仅覆盖本次启动，不修改已保存配置。",
			"按 Ctrl+C 停止服务；不指定命令时默认执行 serve。",
		},
		examples: []string{"serve --data-dir ./data", "serve --data-dir ./data --listen 0.0.0.0:9000"},
	},
	{
		name: "init", description: "初始化管理员密码（需先停止服务）", usage: "init --password-stdin [--data-dir PATH]",
		notes: []string{
			"仅用于首次设置管理员密码；已有管理员请使用 reset-password。",
			"从标准输入读取到 EOF；密码须为 6–72 字节。",
			"必须先停止使用该数据目录的服务。推荐从密码管理器安全管道传入密码。",
		},
		examples: []string{"init --data-dir ./data --password-stdin"},
	},
	{
		name: "reset-password", description: "重置管理员密码（需先停止服务）", usage: "reset-password --password-stdin [--data-dir PATH]",
		notes: []string{
			"必须先停止使用该数据目录的服务；从标准输入读取到 EOF。",
			"新密码须为 6–72 字节，推荐从密码管理器安全管道传入。",
			"保留设备、VNC 凭据、密钥和历史任务；完成后重新启动服务并登录。",
		},
		examples: []string{"reset-password --data-dir ./data --password-stdin"},
	},
	{
		name: "wake", description: "通过运行中的 Gateway 提交设备唤醒任务", usage: "wake --device ID --password-stdin [--url URL]",
		notes: []string{
			"Gateway 服务必须已启动；设备 ID 可在设备导出 JSON 中查看。",
			"从标准输入读取的是管理员登录密码；设备的 VNC 密码使用已保存配置。",
			"密码读取到 EOF，推荐从密码管理器安全管道传入。",
			"提交后输出任务 JSON，不等待唤醒流程完成；结果可在 Web 中查询。",
		},
		examples: []string{"wake --device dev_xxx --password-stdin", "wake --device dev_xxx --url http://192.168.1.1:9000 --password-stdin"},
	},
	{
		name: "check", description: "只读校验配置与设备文件", usage: "check [--data-dir PATH]",
		notes:    []string{"检查 config.json 和 devices.json；不修改数据，无需停止服务。", "不进行网络连通性、管理员密码或 VNC 凭据验证。"},
		examples: []string{"check --data-dir ./data"},
	},
	{
		name: "token", description: "读取本地初始化令牌", usage: "token [--data-dir PATH]",
		notes:    []string{"首次启动服务后，在另一个终端读取令牌并在 Web 中设置管理员密码。", "令牌仅供首次初始化使用，请勿公开；管理员设置完成后不再需要。"},
		examples: []string{"token --data-dir ./data"},
	},
	{
		name: "interfaces", description: "列出可用 IPv4 网卡与广播地址", usage: "interfaces",
		notes:    []string{"以 JSON 输出已启用且具有 IPv4 地址的非回环接口。", "该命令无需启动 Gateway 服务。"},
		examples: []string{"interfaces"},
	},
	{
		name: "version", description: "查看程序版本", usage: "version",
		examples: []string{"version"},
	},
	{
		name: "help", description: "查看总览或指定命令的帮助", usage: "help [命令]",
		notes:    []string{"总览帮助：help、--help、-h。", "命令帮助：help <命令>、<命令> --help、<命令> -h。"},
		examples: []string{"help", "help wake", "serve --help"},
	},
}

func findCommand(name string) *commandHelp {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

func writeHelp(output io.Writer, command string) {
	if command == "" {
		fmt.Fprintf(output, "RemoteWake Gateway v%s - 远程唤醒网关\n\n", strings.TrimPrefix(version, "v"))
		fmt.Fprintln(output, "用法：\n  remotewake-gateway [命令] [参数]\n\n命令：")
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		for _, info := range commands {
			description := info.description
			if info.name == "help" {
				description += "（help [命令]）"
			}
			fmt.Fprintf(table, "  %s\t%s\n", info.name, description)
		}
		table.Flush()
		fmt.Fprintln(output, "\n默认行为：\n  不指定命令时执行 serve。\n  数据目录默认 ./data，相对于当前工作目录。\n  首次启动默认监听 127.0.0.1:9000，已有数据读取保存的配置。")
		fmt.Fprintln(output, "\n示例：\n  remotewake-gateway serve --data-dir ./data\n  remotewake-gateway token --data-dir ./data\n  remotewake-gateway help wake")
		fmt.Fprintln(output, "\n使用 remotewake-gateway <命令> --help 查看详细参数。")
		return
	}
	info := findCommand(command)
	fmt.Fprintf(output, "%s\n\n用法：\n  remotewake-gateway %s\n\n参数：\n", info.description, info.usage)
	var options commandOptions
	flags := commandFlags(command, &options)
	metavars := map[string]string{"data-dir": "PATH", "listen": "HOST:PORT", "device": "ID", "url": "URL"}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	flags.VisitAll(func(f *flag.Flag) {
		name, description := "--"+f.Name, f.Usage
		if value := metavars[f.Name]; value != "" {
			name += " " + value
		}
		if f.DefValue != "" && f.DefValue != "false" {
			description += "（默认：" + f.DefValue + "）"
		}
		fmt.Fprintf(table, "  %s\t%s\n", name, description)
	})
	fmt.Fprintln(table, "  -h, --help\t查看本命令帮助")
	table.Flush()
	if len(info.notes) > 0 {
		fmt.Fprintln(output, "\n说明：")
		for _, note := range info.notes {
			fmt.Fprintln(output, "  "+note)
		}
	}
	fmt.Fprintln(output, "\n示例：")
	for _, example := range info.examples {
		fmt.Fprintln(output, "  remotewake-gateway "+example)
	}
}
