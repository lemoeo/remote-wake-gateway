package httptransport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
)

type streamMessage struct{ kind, data string }

func TestSSEAuthenticationUpdatesTimeoutAndLogout(t *testing.T) {
	fs, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	service, err := app.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	a, err := auth.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Setup(a.Token(), "test12"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login("test12")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(service, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	api.streamHeartbeat = 20 * time.Millisecond
	server := httptest.NewUnstartedServer(api.Handler())
	server.Config.WriteTimeout = 30 * time.Millisecond
	server.Config.RegisterOnShutdown(service.CloseStreams)
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := func(cookie, origin string) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/events", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	for _, tc := range []struct {
		cookie, origin string
		status         int
	}{{"", "", 401}, {token, "https://attacker.example", 403}} {
		res, err := server.Client().Do(request(tc.cookie, tc.origin))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("status: %d", res.StatusCode)
		}
	}
	r := request(token, server.URL)
	res, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream headers: %v", res.Header)
	}
	messages := make(chan streamMessage, 32)
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(res.Body)
		var m streamMessage
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				select {
				case messages <- m:
				case <-ctx.Done():
					return
				}
				m = streamMessage{}
			}
			if strings.HasPrefix(line, "event: ") {
				m.kind = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				m.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	next := func(want string) streamMessage {
		t.Helper()
		for {
			select {
			case m, ok := <-messages:
				if !ok {
					t.Fatal("SSE disconnected unexpectedly")
				}
				if m.kind == want {
					return m
				}
				if m.kind != "" {
					t.Fatalf("unexpected event %s", m.kind)
				}
			case <-ctx.Done():
				t.Fatal("SSE timed out")
				return streamMessage{}
			}
		}
	}
	initial := next("resync")
	if initial.data != "{\"ready\":true}" {
		t.Fatalf("unexpected initial payload: %s", initial.data)
	}
	// Exceed the server's normal response deadline: streaming must stay alive.
	time.Sleep(80 * time.Millisecond)
	d := model.Device{Name: "SSE test", Type: "pc", Enabled: true, WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:55", Broadcast: "255.255.255.255", Port: 9, Repeat: 1, IntervalMS: 100}}
	if _, err = service.SaveDevice(d, nil, false, true); err != nil {
		t.Fatal(err)
	}
	changed := next("devices.changed")
	var event app.Event
	if err = json.Unmarshal([]byte(changed.data), &event); err != nil || event.Revision != service.Devices().Revision {
		t.Fatalf("device update: %s", changed.data)
	}
	a.Logout(r)
	next("auth.expired")
	select {
	case _, ok := <-messages:
		if ok {
			t.Fatal("stream remained open after logout")
		}
	case <-ctx.Done():
		t.Fatal("stream remained open after logout")
	}
}

func TestSSEShutdownReleasesLongRequest(t *testing.T) {
	fs, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	service, err := app.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	a, err := auth.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Setup(a.Token(), "test12"); err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Login("test12")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(service, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(api.Handler())
	server.Config.RegisterOnShutdown(service.CloseStreams)
	server.Start()
	defer server.Close()
	r, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	res, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = server.Config.Shutdown(ctx); err != nil {
		t.Fatalf("stream prevented shutdown: %v", err)
	}
	if _, err = io.Copy(io.Discard, res.Body); err != nil {
		t.Fatal(err)
	}
}
