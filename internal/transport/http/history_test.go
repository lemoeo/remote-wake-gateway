package httptransport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
)

func TestHistoryCleanupAPIAuthPreviewAndPersistence(t *testing.T) {
	fs, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	now := time.Now().UTC()
	for _, j := range []model.Job{
		{SchemaVersion: model.Schema, ID: "job_history", CommandID: "cmd_history", DeviceID: "fixture", Status: "success", CreatedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Hour)},
		{SchemaVersion: model.Schema, ID: "job_protected", CommandID: "cmd_protected", DeviceID: "fixture", Status: "failed", CreatedAt: now, FinishedAt: now, ExpiresAt: now.Add(time.Minute)},
	} {
		if err = fs.Write("jobs/"+j.ID+".json", j); err != nil {
			t.Fatal(err)
		}
	}
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
	token, session, err := a.Login("test12")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(service, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	call := func(method, path string, body any, authenticated bool, csrf, origin string) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		r, err := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, b
	}
	if status, _ := call("GET", "/wake-jobs/cleanup?scope=completed", nil, false, "", ""); status != 401 {
		t.Fatal(status)
	}
	status, b := call("GET", "/wake-jobs/cleanup?scope=completed", nil, true, "", "")
	if status != 200 {
		t.Fatalf("preview: %d %s", status, b)
	}
	var plan app.HistoryCleanup
	if err = json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Eligible != 1 || plan.Protected != 1 {
		t.Fatalf("preview: %+v", plan)
	}
	input := map[string]any{"scope": plan.Scope, "before": plan.Before}
	for _, tc := range []struct {
		csrf, origin string
		status       int
	}{{"wrong", "", 403}, {session.CSRF, "https://attacker.example", 403}} {
		if status, _ = call("POST", "/wake-jobs/cleanup", input, true, tc.csrf, tc.origin); status != tc.status {
			t.Fatal(status)
		}
	}
	if status, _ = call("GET", "/wake-jobs/job_history", nil, true, "", ""); status != 200 {
		t.Fatal("preview or rejected request deleted history")
	}
	if status, _ = call("GET", "/wake-jobs/cleanup?scope=unknown", nil, true, "", ""); status != 422 {
		t.Fatal(status)
	}
	if status, _ = call("POST", "/wake-jobs/cleanup", map[string]any{"scope": "completed"}, true, session.CSRF, ""); status != 422 {
		t.Fatal(status)
	}
	status, b = call("POST", "/wake-jobs/cleanup", input, true, session.CSRF, "")
	if status != 200 {
		t.Fatalf("cleanup: %d %s", status, b)
	}
	var result app.HistoryCleanup
	if err = json.Unmarshal(b, &result); err != nil || result.Deleted != 1 {
		t.Fatalf("cleanup: %s", b)
	}
	if status, _ = call("GET", "/wake-jobs/job_history", nil, true, "", ""); status != 404 {
		t.Fatal(status)
	}
	if status, _ = call("GET", "/wake-jobs/job_protected", nil, true, "", ""); status != 200 {
		t.Fatal("protected task deleted")
	}
	status, b = call("POST", "/wake-jobs/cleanup", input, true, session.CSRF, "")
	if status != 200 {
		t.Fatal(status)
	}
	if err = json.Unmarshal(b, &result); err != nil || result.Deleted != 0 {
		t.Fatal("cleanup was not idempotent")
	}
	cfg := service.Settings()
	cfg.MaxJobs = 500
	status, b = call("PUT", "/settings", cfg, true, session.CSRF, "")
	if status != 200 || !bytes.Contains(b, []byte(`"restart_required":false`)) {
		t.Fatalf("max_jobs: %d %s", status, b)
	}
	service.Close()
	reopened, err := app.New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.Jobs()) != 1 || reopened.Config().MaxJobs != 500 {
		t.Fatal("cleanup or max_jobs not persisted")
	}
}
