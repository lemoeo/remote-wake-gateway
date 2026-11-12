package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

func seedHistory(t *testing.T, s *Service, id, status string, finished, expires time.Time) model.Job {
	t.Helper()
	j := model.Job{SchemaVersion: model.Schema, ID: id, CommandID: "cmd_" + id, DeviceID: "history_device", Status: status, Action: "wake", FinishedAt: finished, CreatedAt: finished.Add(-time.Minute), ExpiresAt: expires, Steps: []model.Step{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fs.Write(filepath.Join("jobs", id+".json"), j); err != nil {
		t.Fatal(err)
	}
	s.jobs[id] = &j
	s.indexJobLocked(j)
	s.commands[j.CommandID] = id
	return j
}

func TestHistoryDeletionDoesNotHoldApplicationLock(t *testing.T) {
	s, _ := testService(t)
	d := addMac(t, s, "live_during_cleanup")
	s.VNC = func(context.Context, macvnc.Options) (macvnc.Evidence, error) {
		return macvnc.Evidence{Authenticated: true}, nil
	}
	now := time.Now().Add(-time.Hour)
	seedHistory(t, s, "job_slow_delete", "success", now, now)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	s.removeJobFile = func(path string) error { close(entered); <-release; return os.Remove(path) }
	plan, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.CleanupHistory(context.Background(), plan.Scope, plan.Before); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup not started")
	}
	if _, err = s.CleanupHistory(context.Background(), plan.Scope, plan.Before); code(err) != "cleanup_busy" {
		t.Fatal("parallel cleanup allowed")
	}
	reads := make(chan error, 1)
	go func() {
		if len(s.Devices().Devices) != 1 {
			reads <- fmt.Errorf("device snapshot lost")
			return
		}
		if _, err := s.ListJobs(JobQuery{Limit: 50}); err != nil {
			reads <- err
			return
		}
		j, err := s.Submit(context.Background(), command(d.ID, "during_cleanup"))
		if err == nil {
			_, err = s.Get(j.ID)
		}
		reads <- err
	}()
	select {
	case err := <-reads:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("file deletion blocked readers or task submission")
	}
	once.Do(func() { close(release) })
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticHistorySweepIsBoundedAndReportsExactIDs(t *testing.T) {
	s, _ := testService(t)
	d := addMac(t, s, "bounded_sweep")
	s.VNC = func(context.Context, macvnc.Options) (macvnc.Evidence, error) {
		return macvnc.Evidence{Authenticated: true}, nil
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	for i := range historySweepBatch + 9 {
		seedHistory(t, s, fmt.Sprintf("job_expired_%03d", i), "success", old, old)
	}
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	j, err := s.Submit(context.Background(), command(d.ID, "bounded_sweep_cmd"))
	if err != nil {
		t.Fatal(err)
	}
	event := nextEvent(t, stream)
	if event.Type != "history.removed" || len(event.JobIDs) != historySweepBatch {
		t.Fatalf("sweep notification: %+v", event)
	}
	remaining := 0
	for _, j := range s.Jobs() {
		if j.CreatedAt.Before(time.Now().Add(-7 * 24 * time.Hour)) {
			remaining++
		}
	}
	if remaining != 9 {
		t.Fatalf("unbounded sweep: remaining=%d", remaining)
	}
	retry, err := s.Submit(context.Background(), command(d.ID, "bounded_sweep_cmd"))
	if err != nil || retry.ID != j.ID {
		t.Fatal("known-command retry changed during cleanup")
	}
	if len(s.Jobs()) != 10 {
		t.Fatal("known-command retry ran another sweep")
	}
}

func TestHistoryCleanupPreviewScopesAndReplayProtection(t *testing.T) {
	s, fs := testService(t)
	addMac(t, s, "history_device")
	now := time.Now().UTC()
	old := seedHistory(t, s, "job_old", "success", now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour))
	recent := seedHistory(t, s, "job_recent", "failed", now.Add(-time.Hour), now.Add(-time.Hour))
	protected := seedHistory(t, s, "job_protected", "success", now.Add(-time.Second), now.Add(time.Minute))
	seedHistory(t, s, "job_active", "running", time.Time{}, now.Add(time.Minute))
	seedHistory(t, s, "job_queued", "pending", time.Time{}, now.Add(time.Minute))
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	plan, err := s.PreviewHistoryCleanup("expired")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible != 1 || plan.Active != 2 || plan.Protected != 1 {
		t.Fatalf("preview: %+v", plan)
	}
	if _, err = os.Stat(fs.Path("jobs/" + old.ID + ".json")); err != nil {
		t.Fatal("preview changed disk")
	}
	result, err := s.CleanupHistory(context.Background(), plan.Scope, plan.Before)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("cleanup: %+v", result)
	}
	if _, err = s.Get(old.ID); code(err) != "job_not_found" {
		t.Fatal("deleted job still queryable")
	}
	if _, err = os.Stat(fs.Path("jobs/" + old.ID + ".json")); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if _, ok := s.commands[old.CommandID]; ok {
		t.Fatal("command index retained")
	}
	if e := nextEvent(t, stream); e.Type != "history.cleared" || e.Cleanup.Deleted != 1 {
		t.Fatalf("event: %+v", e)
	}
	all, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	if all.Eligible != 1 {
		t.Fatalf("recent terminal task not offered: %+v", all)
	}
	if _, err = s.CleanupHistory(context.Background(), all.Scope, all.Before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(recent.ID); code(err) != "job_not_found" {
		t.Fatal(err)
	}
	if _, err = s.Get(protected.ID); err != nil {
		t.Fatal("protected result removed")
	}
	if len(s.Jobs()) != 3 {
		t.Fatalf("active/protected tasks not preserved: %d", len(s.Jobs()))
	}
	d, _ := s.device("history_device")
	if !s.creds.Has(d) {
		t.Fatal("credentials changed")
	}
	if len(s.Devices().Devices) != 1 {
		t.Fatal("device configuration changed")
	}
}

func TestCleanupConfirmationDoesNotExpandAfterPreview(t *testing.T) {
	s, _ := testService(t)
	now := time.Now().UTC()
	seedHistory(t, s, "job_selected", "success", now.Add(-time.Hour), now.Add(-time.Hour))
	plan, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate tasks finishing (or leaving protection) after the preview.
	seedHistory(t, s, "job_later", "success", plan.Before.Add(time.Nanosecond), now.Add(-time.Hour))
	seedHistory(t, s, "job_leaving_protection", "success", now.Add(-time.Minute), plan.Before.Add(-time.Minute+time.Nanosecond))
	result, err := s.CleanupHistory(context.Background(), plan.Scope, plan.Before)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || len(s.Jobs()) != 2 {
		t.Fatalf("preview widened: %+v", result)
	}
	for _, scope := range []string{"", "all", "../jobs"} {
		if _, err = s.PreviewHistoryCleanup(scope); code(err) != "invalid_cleanup_scope" {
			t.Fatalf("invalid scope: %v", err)
		}
	}
	for _, before := range []time.Time{{}, time.Now().Add(time.Hour)} {
		if _, err = s.CleanupHistory(context.Background(), "completed", before); code(err) != "invalid_cleanup_request" {
			t.Fatal(err)
		}
	}
}

func TestHistoryCleanupFailureKeepsIndexesConsistent(t *testing.T) {
	s, fs := testService(t)
	now := time.Now().Add(-time.Hour)
	seedHistory(t, s, "job_a_removed", "success", now, now)
	blocked := seedHistory(t, s, "job_b_blocked", "failed", now, now)
	path := fs.Path("jobs/" + blocked.ID + ".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	plan, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.CleanupHistory(context.Background(), plan.Scope, plan.Before)
	if code(err) != "storage_unavailable" || result.Deleted != 1 || s.Ready() {
		t.Fatalf("partial failure: %+v %v", result, err)
	}
	if _, err = s.Get("job_a_removed"); code(err) != "job_not_found" {
		t.Fatal("removed job retained in memory")
	}
	if _, err = s.Get(blocked.ID); err != nil {
		t.Fatal("failed deletion removed index")
	}
	if _, ok := s.commands[blocked.CommandID]; !ok {
		t.Fatal("failed deletion removed command")
	}
	if e := nextEvent(t, stream); e.Type != "storage.error" {
		t.Fatal(e.Type)
	}
	if e := nextEvent(t, stream); e.Type != "history.invalidated" {
		t.Fatal(e.Type)
	}
}

func TestCanceledHistoryCleanupDoesNotDeleteRecords(t *testing.T) {
	s, _ := testService(t)
	now := time.Now().Add(-time.Hour)
	seedHistory(t, s, "job_cancel_cleanup", "success", now, now)
	plan, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.CleanupHistory(ctx, plan.Scope, plan.Before)
	if code(err) != "cleanup_interrupted" || result.Deleted != 0 || len(s.Jobs()) != 1 || !s.Ready() {
		t.Fatalf("cancel: %+v %v", result, err)
	}
}

func TestMaxJobsSettingValidationAndPersistence(t *testing.T) {
	s, fs := testService(t)
	cfg := s.Settings()
	for _, n := range []int{99, 100001} {
		cfg.MaxJobs = n
		if err := s.UpdateSettings(cfg); code(err) != "invalid_settings" {
			t.Fatalf("invalid max_jobs accepted: %d", n)
		}
	}
	cfg.MaxJobs = 100
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if s.Settings().MaxJobs != 100 || s.Config().MaxJobs != 100 {
		t.Fatal("max_jobs did not apply immediately")
	}
	s.Close()
	reopened, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Config().MaxJobs != 100 {
		t.Fatal("max_jobs not persisted")
	}
}

func TestMaxJobsCannotBeLoweredBelowExistingRecords(t *testing.T) {
	s, _ := testService(t)
	s.mu.Lock()
	for i := range 101 {
		id := fmt.Sprintf("job_%d", i)
		s.jobs[id] = &model.Job{ID: id, Status: "success"}
	}
	s.mu.Unlock()
	cfg := s.Settings()
	cfg.MaxJobs = 100
	if err := s.UpdateSettings(cfg); code(err) != "job_limit_below_usage" {
		t.Fatalf("unsafe limit accepted: %v", err)
	}
	if s.Settings().MaxJobs != 10000 || s.Config().MaxJobs != 10000 {
		t.Fatal("rejected configuration changed capacity")
	}
}

func TestHistoryCleanupPreservesRunningAndQueuedExecution(t *testing.T) {
	original, fs := testService(t)
	cfg := original.Settings()
	original.Close()
	cfg.MaxConcurrent = 1
	cfg.MaxVNC = 1
	if err := fs.Write("config.json", cfg); err != nil {
		t.Fatal(err)
	}
	s, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := addMac(t, s, "history_running")
	second := macDevice("history_queued")
	second.IP = "127.0.0.2"
	password := "secret"
	if _, err = s.SaveDevice(second, &password, false, true); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.VNC = func(ctx context.Context, _ macvnc.Options) (macvnc.Evidence, error) {
		close(started)
		<-ctx.Done()
		return macvnc.Evidence{}, ctx.Err()
	}
	running, err := s.Submit(context.Background(), command(first.ID, "running_cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not started")
	}
	queued, err := s.Submit(context.Background(), command(second.ID, "queued_cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	seedHistory(t, s, "job_cleanup_history", "success", now, now)
	plan, err := s.PreviewHistoryCleanup("completed")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.CleanupHistory(context.Background(), plan.Scope, plan.Before)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Active != 2 {
		t.Fatalf("unexpected cleanup: %+v", result)
	}
	for _, j := range []model.Job{running, queued} {
		got, err := s.Get(j.ID)
		if err != nil || !got.Active() {
			t.Fatalf("active job changed: %+v %v", got, err)
		}
		retry, err := s.Submit(context.Background(), command(j.DeviceID, j.CommandID))
		if err != nil || retry.ID != j.ID {
			t.Fatal("active command lost deduplication")
		}
	}
	if len(s.targets) != 2 || len(s.active) != 2 || len(s.cancels) != 2 {
		t.Fatal("execution locks changed")
	}
}
