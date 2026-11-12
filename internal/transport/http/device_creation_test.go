package httptransport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
)

func TestCreateDeviceHTTPRetryAndIndexedHistoryPages(t *testing.T) {
	fs, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	s, err := app.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := auth.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Setup(a.Token(), "test12"); err != nil {
		t.Fatal(err)
	}
	token, session, err := a.Login("test12")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(s, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	call := func(method, path, key string, body any) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(data))
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", session.CSRF)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	d := model.Device{Name: "Retry PC", Type: "pc", Enabled: true, WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:55"}}
	// Discard the first successful response, as if the connection was lost.
	if status, _ := call("POST", "/devices", "create_lost_response", d); status != 201 {
		t.Fatal(status)
	}
	status, data := call("POST", "/devices", "create_lost_response", d)
	if status != 201 {
		t.Fatalf("retry: %d %s", status, data)
	}
	var saved model.Device
	if err = json.Unmarshal(data, &saved); err != nil || !model.ValidID(saved.ID) {
		t.Fatal("missing generated device ID")
	}
	if len(s.Devices().Devices) != 1 || saved.Revision != 1 {
		t.Fatal("lost-response retry created a duplicate")
	}
	d.Name = "changed draft"
	if status, _ = call("POST", "/devices", "create_lost_response", d); status != 409 {
		t.Fatal("changed retry silently accepted")
	}
	if status, _ = call("POST", "/devices", "bad/key", d); status != 422 {
		t.Fatal("bad request key accepted")
	}

	// Reload a larger persisted history to exercise the index used by HTTP.
	s.Close()
	now := time.Now().UTC().Add(-time.Hour)
	for i := range 61 {
		j := model.Job{SchemaVersion: model.Schema, ID: fmt.Sprintf("job_page_%03d", i), CommandID: fmt.Sprintf("cmd_page_%03d", i), DeviceID: saved.ID, Status: "success", CreatedAt: now.Add(time.Duration(i) * time.Millisecond), FinishedAt: now, ExpiresAt: now}
		if err = fs.Write("jobs/"+j.ID+".json", j); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := app.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	api.App = reloaded
	status, data = call("GET", "/wake-jobs?limit=50", "", nil)
	var page app.JobPage
	if err = json.Unmarshal(data, &page); err != nil || status != 200 || len(page.Jobs) != 50 || page.NextCursor == "" {
		t.Fatalf("first page: %d %s", status, data)
	}
	status, data = call("GET", "/wake-jobs?limit=50&after="+url.QueryEscape(page.NextCursor), "", nil)
	if err = json.Unmarshal(data, &page); err != nil || status != 200 || len(page.Jobs) != 11 || page.NextCursor != "" {
		t.Fatalf("second page: %d %s", status, data)
	}
}
