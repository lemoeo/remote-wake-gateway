package app

import (
	"context"
	"errors"
	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"github.com/lemoeo/remote-wake-gateway/internal/wol"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, *storage.Store) {
	t.Helper()
	fs, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	c := model.DefaultConfig()
	c.AllowedNetworks = append(c.AllowedNetworks, "127.0.0.0/8")
	if e = fs.Write("config.json", c); e != nil {
		t.Fatal(e)
	}
	s, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(); fs.Close() })
	return s, fs
}

func TestCombinedWakeKeepsBothStepResults(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wolOK, vncOK bool
		want         string
	}{{"both", true, true, "success"}, {"wol_fails", false, true, "partial"}, {"vnc_fails", true, false, "partial"}, {"both_fail", false, false, "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testService(t)
			d := addMac(t, s, "combined")
			d.WakeMethod = "wol_mac_vnc"
			interfaces, e := wol.Interfaces()
			if e != nil {
				t.Fatal(e)
			}
			for _, i := range interfaces {
				if i.IP == "127.0.0.1" {
					d.WoL = model.WoL{MAC: "02:11:22:33:44:55", Interface: i.Name, Broadcast: i.Broadcast, Port: 9, Repeat: 1, IntervalMS: 100}
				}
			}
			if d.WoL.Interface == "" {
				t.Skip("no IPv4 loopback")
			}
			if _, e = s.SaveDevice(d, nil, false, false); e != nil {
				t.Fatal(e)
			}
			var sends atomic.Int32
			s.SendWoL = func(context.Context, model.WoL) error {
				sends.Add(1)
				if !tc.wolOK {
					return errors.New("send failed")
				}
				return nil
			}
			s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
				if sends.Load() != 1 {
					t.Error("VNC executed before WoL")
				}
				if e := o.BeforeAuth(); e != nil {
					return macvnc.Evidence{}, e
				}
				if !tc.vncOK {
					return macvnc.Evidence{Connected: true, AuthSent: true}, &macvnc.Failure{Code: "auth_rejected"}
				}
				return macvnc.Evidence{Connected: true, AuthSent: true, Authenticated: true}, nil
			}
			j, e := s.Submit(context.Background(), command(d.ID, "combined_command"))
			if e != nil {
				t.Fatal(e)
			}
			got := waitJob(t, s, j.ID)
			if got.Status != tc.want || len(got.Steps) != 2 {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestProbeDoesNotReleaseAnotherDevicesVNCMutex(t *testing.T) {
	s, _ := testService(t)
	addMac(t, s, "probe_device")
	addMac(t, s, "wake_device")
	addMac(t, s, "duplicate_target")
	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	wakeRelease := make(chan struct{})
	wakeStarted := make(chan struct{})
	s.Probe = func(ctx context.Context, _ string, _ int) error {
		close(probeStarted)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-probeRelease:
			return nil
		}
	}
	s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		close(wakeStarted)
		select {
		case <-ctx.Done():
			return macvnc.Evidence{}, ctx.Err()
		case <-wakeRelease:
			return macvnc.Evidence{Connected: true, Authenticated: true}, nil
		}
	}
	probeCommand := command("probe_device", "probe")
	probeCommand.Action = "probe"
	p, e := s.Submit(context.Background(), probeCommand)
	if e != nil {
		t.Fatal(e)
	}
	<-probeStarted
	w, e := s.Submit(context.Background(), command("wake_device", "wake"))
	if e != nil {
		t.Fatal(e)
	}
	<-wakeStarted
	close(probeRelease)
	waitJob(t, s, p.ID)
	if _, e = s.Submit(context.Background(), command("duplicate_target", "collision")); code(e) != "device_busy" {
		t.Fatalf("probe released a different task's VNC mutex: %v", e)
	}
	close(wakeRelease)
	waitJob(t, s, w.ID)
}

func TestSavedSettingsSurviveRefreshBeforeRestart(t *testing.T) {
	s, _ := testService(t)
	cfg := s.Config()
	cfg.Listen = "127.0.0.1:12345"
	cfg.MaxConcurrent = 5
	if e := s.UpdateSettings(cfg); e != nil {
		t.Fatal(e)
	}
	if s.Settings().Listen != cfg.Listen || s.Settings().MaxConcurrent != 5 {
		t.Fatal("pending settings not returned")
	}
	if s.Config().Listen == cfg.Listen || s.Config().MaxConcurrent == 5 {
		t.Fatal("restart-only settings changed runtime")
	}
}

func TestNewDeviceDefaultsApplyImmediatelyWithoutChangingExistingDevices(t *testing.T) {
	s, fs := testService(t)
	existing, e := s.SaveDevice(model.Device{ID: "existing", Name: "Existing", Enabled: true, WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:55", Broadcast: "192.168.1.255", Port: 7, Repeat: 2}}, nil, false, true)
	if e != nil {
		t.Fatal(e)
	}
	cfg := s.Settings()
	cfg.DeviceDefaults = model.DeviceDefaults{Broadcast: "192.168.2.255", Port: 11, Repeat: 5}
	if e = s.UpdateSettings(cfg); e != nil {
		t.Fatal(e)
	}
	created, e := s.SaveDevice(model.Device{Name: "New", WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:66"}}, nil, false, true)
	if e != nil {
		t.Fatal(e)
	}
	if created.WoL.Broadcast != cfg.DeviceDefaults.Broadcast || created.WoL.Port != 11 || created.WoL.Repeat != 5 {
		t.Fatalf("defaults not applied: %+v", created.WoL)
	}
	for _, d := range s.Devices().Devices {
		if d.ID == existing.ID && (d.WoL.Broadcast != existing.WoL.Broadcast || d.WoL.Port != 7 || d.WoL.Repeat != 2) {
			t.Fatal("changed existing device")
		}
	}
	// Older settings clients omit the new section; they must not reset it.
	legacy := s.Settings()
	legacy.DeviceDefaults = model.DeviceDefaults{}
	if e = s.UpdateSettings(legacy); e != nil {
		t.Fatal(e)
	}
	if s.Settings().DeviceDefaults != cfg.DeviceDefaults {
		t.Fatal("legacy update reset defaults")
	}
	s.Close()
	reopened, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if reopened.Settings().DeviceDefaults != cfg.DeviceDefaults {
		t.Fatal("defaults not persisted")
	}
}

func TestWoLCommandsAcceptSystemSelectionAndCustomBroadcast(t *testing.T) {
	s, _ := testService(t)
	list, e := wol.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	loopback := ""
	for _, i := range list {
		if i.IP == "127.0.0.1" {
			loopback = i.Name
			break
		}
	}
	if loopback == "" {
		t.Skip("no IPv4 loopback")
	}
	var calls atomic.Int32
	s.SendWoL = func(_ context.Context, w model.WoL) error { calls.Add(1); return nil }
	for _, tc := range []struct{ id, nic, target string }{
		{"system_limited", "", "255.255.255.255"},
		{"manual_limited", loopback, "255.255.255.255"},
		{"manual_custom", loopback, "192.168.99.255"},
		{"system_custom", "", "192.168.99.255"},
	} {
		d := model.Device{ID: tc.id, Name: tc.id, Enabled: true, WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:55", Interface: tc.nic, Broadcast: tc.target}}
		if _, e = s.SaveDevice(d, nil, false, true); e != nil {
			t.Fatal(e)
		}
		j, e := s.Submit(context.Background(), command(tc.id, "cmd_"+tc.id))
		if e != nil {
			t.Fatal(e)
		}
		if got := waitJob(t, s, j.ID); got.Status != "success" {
			t.Fatalf("unexpected job: %+v", got)
		}
	}
	d := model.Device{ID: "invalid_nic", Name: "invalid NIC", Enabled: true, WakeMethod: "wol", WoL: model.WoL{MAC: "02:11:22:33:44:55", Interface: "missing-remotewake", Broadcast: "255.255.255.255"}}
	if _, e = s.SaveDevice(d, nil, false, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Submit(context.Background(), command(d.ID, "invalid_nic_command")); code(e) != "invalid_interface" {
		t.Fatalf("explicit missing NIC was not rejected: %v", e)
	}
	if calls.Load() != 4 {
		t.Fatalf("unexpected network calls: %d", calls.Load())
	}
}

func TestGeneratedDeviceIDKeepsCredentialsAndHistoryAcrossRenameAndRestart(t *testing.T) {
	s, fs := testService(t)
	password := "private-mac-password"
	d := macDevice("")
	d.Name = "客厅 Mac mini"
	d.Type = "mac"
	first, e := s.SaveDevice(d, &password, false, true)
	if e != nil || first.ID == "" || !model.ValidID(first.ID) {
		t.Fatalf("ID not generated: %+v %v", first, e)
	}
	second, e := s.SaveDevice(d, &password, false, true)
	if e != nil || second.ID == first.ID {
		t.Fatalf("same name must not reuse an identity: %v", e)
	}
	s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		if e := o.BeforeAuth(); e != nil {
			return macvnc.Evidence{}, e
		}
		return macvnc.Evidence{Connected: true, AuthSent: true, Authenticated: true}, nil
	}
	j, e := s.Submit(context.Background(), command(first.ID, "auto_id_wake"))
	if e != nil {
		t.Fatal(e)
	}
	if got := waitJob(t, s, j.ID); got.Status != "success" {
		t.Fatalf("wake by generated ID failed: %+v", got)
	}
	first.Name = "书房 Mac mini"
	first.Type = "nas"
	renamed, e := s.SaveDevice(first, nil, false, false)
	if e != nil || renamed.ID != first.ID || !renamed.PasswordConfigured {
		t.Fatalf("identity or credentials lost on rename: %+v %v", renamed, e)
	}
	blank := renamed
	blank.ID = ""
	if _, e = s.SaveDevice(blank, nil, false, false); code(e) != "invalid_device" {
		t.Fatalf("update without an ID should not create a device: %v", e)
	}
	s.Close()
	reopened, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	var saved model.Device
	for _, v := range reopened.Devices().Devices {
		if v.ID == first.ID {
			saved = v
		}
	}
	if saved.Name != renamed.Name || saved.Type != "nas" || !saved.PasswordConfigured {
		t.Fatalf("device did not survive restart: %+v", saved)
	}
	secret, e := reopened.creds.Get(saved)
	defer clear(secret)
	if e != nil || string(secret) != password {
		t.Fatal("credential binding lost after restart")
	}
	history, e := reopened.Get(j.ID)
	if e != nil || history.DeviceID != saved.ID {
		t.Fatal("history no longer points to the renamed device")
	}
}
func macDevice(id string) model.Device {
	return model.Device{ID: id, Name: id, Enabled: true, WakeMethod: "mac_vnc", IP: "127.0.0.1", VNC: model.VNC{Port: 5900, Username: "alice"}}
}
func addMac(t *testing.T, s *Service, id string) model.Device {
	t.Helper()
	p := "secret"
	d, e := s.SaveDevice(macDevice(id), &p, false, true)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func command(device, key string) model.Command {
	now := time.Now().UTC()
	return model.Command{SchemaVersion: model.Schema, CommandID: key, DeviceID: device, Action: "wake", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
}
func waitJob(t *testing.T, s *Service, id string) model.Job {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		j, e := s.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if !j.Active() {
			return j
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("job not finished")
	return model.Job{}
}
func code(e error) string {
	var f *model.Fault
	if errors.As(e, &f) {
		return f.Code
	}
	return ""
}
func TestIdempotencyBusyAndDetachedRequest(t *testing.T) {
	s, _ := testService(t)
	addMac(t, s, "mac")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		calls.Add(1)
		if e := o.BeforeAuth(); e != nil {
			return macvnc.Evidence{}, e
		}
		close(started)
		select {
		case <-ctx.Done():
			return macvnc.Evidence{AuthSent: true}, ctx.Err()
		case <-release:
			return macvnc.Evidence{AuthSent: true, Authenticated: true}, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	j, e := s.Submit(ctx, command("mac", "same"))
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	<-started
	j2, e := s.Submit(context.Background(), command("mac", "same"))
	if e != nil || j2.ID != j.ID {
		t.Fatal("retry created another job")
	}
	if _, e = s.Submit(context.Background(), command("mac", "different")); code(e) != "device_busy" {
		t.Fatalf("busy not enforced: %v", e)
	}
	d := s.Devices().Devices[0]
	if _, e = s.SaveDevice(d, nil, false, false); code(e) != "device_busy" {
		t.Fatal("edited active device")
	}
	close(release)
	j = waitJob(t, s, j.ID)
	if j.Status != "success" || calls.Load() != 1 {
		t.Fatalf("detached or dedup failed: %+v calls %d", j, calls.Load())
	}
	cmd := command("another", "same")
	if _, e = s.Submit(context.Background(), cmd); code(e) != "idempotency_conflict" {
		t.Fatal("conflicting id accepted")
	}
}
func TestCooldownAcrossCommandsAndRestart(t *testing.T) {
	s, fs := testService(t)
	addMac(t, s, "mac")
	s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		if e := o.BeforeAuth(); e != nil {
			return macvnc.Evidence{}, e
		}
		return macvnc.Evidence{Connected: true, AuthSent: true}, &macvnc.Failure{Code: "auth_rejected"}
	}
	j, e := s.Submit(context.Background(), command("mac", "first"))
	if e != nil {
		t.Fatal(e)
	}
	if waitJob(t, s, j.ID).Status != "failed" {
		t.Fatal("wrong status")
	}
	if _, e = s.Submit(context.Background(), command("mac", "next")); code(e) != "authentication_cooldown" {
		t.Fatal("cooldown bypassed")
	}
	s.Close()
	next, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e = next.Submit(context.Background(), command("mac", "after_restart")); code(e) != "authentication_cooldown" {
		t.Fatal("cooldown lost on restart")
	}
	retry, e := next.Submit(context.Background(), command("mac", "first"))
	if e != nil || retry.ID != j.ID {
		t.Fatal("dedup lost on restart")
	}
}
func TestRecoveryNeverReplaysInterruptedJobs(t *testing.T) {
	s, fs := testService(t)
	d := addMac(t, s, "mac")
	s.Close()
	j := model.Job{SchemaVersion: model.Schema, ID: "job_crashed", CommandID: "cmd_crashed", DeviceID: d.ID, Action: "wake", Status: "running", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	if e := fs.Write(filepath.Join("jobs", j.ID+".json"), j); e != nil {
		t.Fatal(e)
	}
	p := Policy{SchemaVersion: model.Schema, Targets: map[string]Guard{d.TargetKey(): {InFlight: true}}}
	fs.Write("runtime-policy.json", p)
	next, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	got, e := next.Get(j.ID)
	if e != nil || got.Status != "interrupted" {
		t.Fatal("did not interrupt stale job")
	}
	if _, e = next.Submit(context.Background(), command(d.ID, "cmd_new")); code(e) != "authentication_cooldown" {
		t.Fatal("in-flight guard not recovered")
	}
}
func TestFailedPendingWriteDoesNotExecute(t *testing.T) {
	s, fs := testService(t)
	addMac(t, s, "mac")
	var calls atomic.Int32
	s.VNC = func(context.Context, macvnc.Options) (macvnc.Evidence, error) {
		calls.Add(1)
		return macvnc.Evidence{}, nil
	}
	os.Rename(fs.Path("jobs"), fs.Path("jobs.saved"))
	os.WriteFile(fs.Path("jobs"), []byte("not a directory"), 0600)
	if _, e := s.Submit(context.Background(), command("mac", "diskfull")); code(e) != "storage_unavailable" {
		t.Fatalf("wanted disk error: %v", e)
	}
	if calls.Load() != 0 || s.Ready() {
		t.Fatal("executed despite persistence failure")
	}
}
func TestCancelAndCredentialRebind(t *testing.T) {
	s, _ := testService(t)
	d := addMac(t, s, "mac")
	d.IP = "127.0.0.2"
	if _, e := s.SaveDevice(d, nil, false, false); code(e) != "credentials_rebind_required" {
		t.Fatal("rebind guard missing")
	}
	s.VNC = func(ctx context.Context, o macvnc.Options) (macvnc.Evidence, error) {
		<-ctx.Done()
		return macvnc.Evidence{}, ctx.Err()
	}
	j, e := s.Submit(context.Background(), command("mac", "cancel"))
	if e != nil {
		t.Fatal(e)
	}
	s.Cancel(j.ID)
	if waitJob(t, s, j.ID).Status != "cancelled" {
		t.Fatal("cancel failed")
	}
}
