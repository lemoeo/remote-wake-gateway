package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedDeviceAndJobFlow(t *testing.T) {
	fs, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	cfg := model.DefaultConfig()
	cfg.AllowedNetworks = append(cfg.AllowedNetworks, "127.0.0.0/8")
	fs.Write("config.json", cfg)
	service, e := app.New(fs)
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	service.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		if e := o.BeforeAuth(); e != nil {
			return macvnc.Evidence{}, e
		}
		return macvnc.Evidence{Connected: true, AuthSent: true, Authenticated: true}, nil
	}
	a, e := auth.New(fs)
	if e != nil {
		t.Fatal(e)
	}
	api, e := New(service, a, "test")
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	call := func(method, path string, body any, headers map[string]string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, reader)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, data
	}
	if status, _ := call("GET", "/devices", nil, nil); status != 401 {
		t.Fatal("devices exposed without auth")
	}
	if status, _ := call("POST", "/auth/setup", map[string]string{"token": a.Token(), "password": "test-admin-password"}, map[string]string{"Origin": "https://attacker.example"}); status != 403 {
		t.Fatal("cross-origin setup accepted")
	}
	if status, b := call("POST", "/auth/setup", map[string]string{"token": a.Token(), "password": "test-admin-password"}, nil); status != 201 {
		t.Fatalf("setup: %d %s", status, b)
	}
	if status, _ := call("POST", "/auth/setup", map[string]string{}, nil); status != 409 {
		t.Fatal("setup still open")
	}
	status, b := call("POST", "/auth/session", map[string]string{"password": "test-admin-password"}, nil)
	if status != 200 {
		t.Fatalf("login: %s", b)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(b, &session)
	csrf = session.CSRF
	d := model.Device{Name: "Test Mac", Type: "mac", WakeMethod: "mac_vnc", Enabled: true, IP: "127.0.0.1", VNC: model.VNC{Port: 5900, Username: "alice"}}
	input := map[string]any{}
	deviceJSON, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(deviceJSON, &input); e != nil {
		t.Fatal(e)
	}
	delete(input, "id") // The Web form does not send an ID when creating a device.
	input["vnc_password"] = "private-mac-password"
	if status, _ := call("POST", "/devices", input, map[string]string{"X-CSRF-Token": "wrong"}); status != 403 {
		t.Fatal("CSRF bypass")
	}
	if status, b = call("POST", "/devices", input, nil); status != 201 {
		t.Fatalf("device: %d %s", status, b)
	}
	var created model.Device
	if e = json.Unmarshal(b, &created); e != nil {
		t.Fatal(e)
	}
	if !model.ValidID(created.ID) || created.ID == "" || created.Type != "mac" {
		t.Fatalf("missing generated ID or lost device type: %+v", created)
	}
	if strings.Contains(string(b), "private-mac-password") {
		t.Fatal("password leaked in response")
	}
	status, b = call("POST", "/devices/"+created.ID+"/wake", nil, map[string]string{"Idempotency-Key": "same-command"})
	if status != 202 {
		t.Fatalf("wake: %d %s", status, b)
	}
	var j model.Job
	json.Unmarshal(b, &j)
	time.Sleep(30 * time.Millisecond)
	_, b = call("POST", "/devices/"+created.ID+"/wake", nil, map[string]string{"Idempotency-Key": "same-command"})
	var retry model.Job
	json.Unmarshal(b, &retry)
	if retry.ID != j.ID {
		t.Fatal("duplicate executed")
	}
	status, b = call("GET", "/wake-jobs/"+j.ID, nil, nil)
	if status != 200 || bytes.Contains(b, []byte("private-mac-password")) {
		t.Fatal("job failed or secret leaked")
	}
	status, b = call("GET", "/devices/export", nil, nil)
	if status != 200 || bytes.Contains(b, []byte("private-mac-password")) || bytes.Contains(b, []byte("ciphertext")) {
		t.Fatal("export leaked secrets")
	}
	var exported model.DeviceFile
	if e = json.Unmarshal(b, &exported); e != nil {
		t.Fatal(e)
	}
	if len(exported.Devices) != 1 || exported.Devices[0].ID != created.ID || exported.Devices[0].Type != "mac" {
		t.Fatal("export omitted internal ID or device type")
	}
	updated := service.Settings()
	updated.DeviceDefaults = model.DeviceDefaults{Broadcast: "127.0.0.1", Port: 7, Repeat: 2}
	status, b = call("PUT", "/settings", updated, nil)
	if status != 200 {
		t.Fatalf("default settings update: %d %s", status, b)
	}
	var saved struct {
		RestartRequired bool `json:"restart_required"`
	}
	if e = json.Unmarshal(b, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.RestartRequired {
		t.Fatal("default-only update incorrectly requires restart")
	}
	_, b = call("GET", "/settings", nil, nil)
	var current model.Config
	if e = json.Unmarshal(b, &current); e != nil || current.DeviceDefaults != updated.DeviceDefaults {
		t.Fatal("settings API did not expose saved defaults")
	}
	status, _ = call("DELETE", "/auth/session", nil, nil)
	if status != 204 {
		t.Fatal("logout failed")
	}
	if status, _ = call("GET", "/devices", nil, nil); status != 401 {
		t.Fatal("session survives logout")
	}
}
