package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	httptransport "github.com/lemoeo/remote-wake-gateway/internal/transport/http"
	"github.com/lemoeo/remote-wake-gateway/internal/wol"
)

var version = "0.1.0-dev"

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	if command == "version" {
		fmt.Println("RemoteWake Gateway", version)
		return nil
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := flags.String("data-dir", "./data", "数据目录")
	listen := flags.String("listen", "", "HTTP 监听地址（仅本次生效）")
	device := flags.String("device", "", "设备 ID")
	stdin := flags.Bool("password-stdin", false, "从标准输入读取管理密码")
	apiURL := flags.String("url", "http://127.0.0.1:9000", "正在运行的 Gateway URL")
	if e := flags.Parse(args); e != nil {
		return e
	}
	switch command {
	case "interfaces":
		v, e := wol.Interfaces()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "token":
		b, e := os.ReadFile(filepath.Join(*dir, "secrets/setup.token"))
		if e != nil {
			return fmt.Errorf("初始化令牌不存在（管理员可能已设置），请检查服务和数据目录")
		}
		fmt.Print(string(b))
		return nil
	case "wake":
		if !*stdin || !model.ValidID(*device) {
			return errors.New("用法: wake --device ID --password-stdin [--url http://127.0.0.1:9000]")
		}
		password, e := readPassword()
		if e != nil {
			return e
		}
		return wake(*apiURL, *device, password)
	case "check":
		var c model.Config
		b, e := os.ReadFile(filepath.Join(*dir, "config.json"))
		if e != nil {
			return e
		}
		if e = storage.Decode(b, &c); e != nil {
			return e
		}
		if e = c.Validate(); e != nil {
			return e
		}
		var d model.DeviceFile
		b, e = os.ReadFile(filepath.Join(*dir, "devices.json"))
		if errors.Is(e, os.ErrNotExist) {
			fmt.Println("配置有效；尚无已保存设备")
			return nil
		}
		if e != nil {
			return e
		}
		if e = storage.Decode(b, &d); e != nil {
			return e
		}
		if d.SchemaVersion != model.Schema {
			return errors.New("设备文件版本不支持")
		}
		for _, v := range d.Devices {
			v.Defaults()
			if e = v.Validate(c); e != nil {
				return e
			}
		}
		fmt.Println("配置和设备校验通过")
		return nil
	case "init", "reset-password", "serve":
	default:
		return errors.New("可用命令: serve、init、reset-password、token、check、interfaces、wake、version")
	}
	fs, e := storage.Open(*dir)
	if e != nil {
		return e
	}
	defer fs.Close()
	if command == "reset-password" {
		if !*stdin {
			return errors.New("reset-password 需要 --password-stdin；请先停止使用该数据目录的服务")
		}
		password, e := readPassword()
		if e != nil {
			return e
		}
		if e = auth.ResetPassword(fs, password); e != nil {
			return e
		}
		fmt.Println("管理员密码已重置；设备、VNC 凭据和历史记录已保留，请重新启动服务并登录")
		return nil
	}
	if command == "init" {
		if !*stdin {
			return errors.New("init 需要 --password-stdin；服务必须停止")
		}
		password, e := readPassword()
		if e != nil {
			return e
		}
		a, e := auth.New(fs)
		if e != nil {
			return e
		}
		if e = a.Setup(a.Token(), password); e != nil {
			return e
		}
		fmt.Println("管理员密码已设置")
		return nil
	}
	service, e := app.New(fs)
	if e != nil {
		return e
	}
	defer service.Close()
	a, e := auth.New(fs)
	if e != nil {
		return e
	}
	api, e := httptransport.New(service, a, version)
	if e != nil {
		return e
	}
	address := service.Config().Listen
	if *listen != "" {
		address = *listen
	}
	server := &http.Server{Addr: address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errch := make(chan error, 1)
	go func() { errch <- server.ListenAndServe() }()
	slog.Info("RemoteWake Gateway 启动", "version", version, "listen", address, "data_dir", fs.Dir)
	if !a.Initialized() {
		slog.Info("请运行 remotewake-gateway token --data-dir PATH 获取初始化令牌")
	}
	select {
	case e := <-errch:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
func readPassword() (string, error) {
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 74))
	if e != nil {
		return "", e
	}
	defer clear(b)
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if len(s) < 1 || len(s) > 72 {
		return "", errors.New("密码长度无效")
	}
	return s, nil
}
func wake(base, id, password string) error {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path string, v any, csrf, key string) ([]byte, error) {
		b, _ := json.Marshal(v)
		req, e := http.NewRequest(method, strings.TrimRight(base, "/")+path, bytes.NewReader(b))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer res.Body.Close()
		data, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if e != nil {
			return nil, e
		}
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("API 返回 %d: %s", res.StatusCode, data)
		}
		return data, nil
	}
	data, e := call("POST", "/api/v1/auth/session", map[string]string{"password": password}, "", "")
	if e != nil {
		return e
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if e = json.Unmarshal(data, &session); e != nil {
		return e
	}
	data, e = call("POST", "/api/v1/devices/"+id+"/wake", nil, session.CSRF, model.ID("cmd_"))
	if e != nil {
		return e
	}
	fmt.Println(string(data))
	return nil
}
