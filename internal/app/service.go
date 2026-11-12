package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/credentials"
	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/probe"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"github.com/lemoeo/remote-wake-gateway/internal/wol"
)

type Guard struct {
	Until    time.Time `json:"until"`
	InFlight bool      `json:"in_flight"`
	Blocked  bool      `json:"blocked"`
}
type Policy struct {
	SchemaVersion int              `json:"schema_version"`
	Targets       map[string]Guard `json:"targets"`
}
type task struct {
	id     string
	device model.Device
	ctx    context.Context
	cancel context.CancelFunc
}
type Service struct {
	mu               sync.Mutex
	fs               *storage.Store
	cfg              model.Config
	desired          model.Config
	creds            *credentials.Store
	devices          model.DeviceFile
	jobs             map[string]*model.Job
	jobOrder         []jobKey
	cleanupMu        sync.Mutex
	nextHistorySweep time.Time
	removeJobFile    func(string) error
	commands         map[string]string
	active           map[string]string
	targets          map[string]string
	cancels          map[string]context.CancelFunc
	policy           Policy
	queue            chan task
	vncSlots         chan struct{}
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	closed           bool
	storageErr       error
	subscribers      map[chan Event]struct{}
	eventsClosed     bool
	// Network adapters are replaceable in tests and independent of HTTP/MQTT.
	SendWoL func(context.Context, model.WoL) error
	VNC     func(context.Context, macvnc.Options) (macvnc.Evidence, error)
	Probe   func(context.Context, string, int) error
}

func LoadConfig(fs *storage.Store) (model.Config, error) {
	var c model.Config
	err := fs.Read("config.json", &c)
	if errors.Is(err, os.ErrNotExist) {
		c = model.DefaultConfig()
		err = fs.Write("config.json", c)
	}
	if err != nil {
		return c, err
	}
	c.Defaults()
	return c, c.Validate()
}
func New(fs *storage.Store) (*Service, error) {
	cfg, err := LoadConfig(fs)
	if err != nil {
		return nil, err
	}
	creds, err := credentials.Open(fs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{fs: fs, cfg: cfg, creds: creds, devices: model.DeviceFile{SchemaVersion: model.Schema, Revision: 1, Devices: []model.Device{}}, jobs: map[string]*model.Job{}, commands: map[string]string{}, active: map[string]string{}, targets: map[string]string{}, cancels: map[string]context.CancelFunc{}, policy: Policy{SchemaVersion: model.Schema, Targets: map[string]Guard{}}, queue: make(chan task, cfg.QueueSize), vncSlots: make(chan struct{}, cfg.MaxVNC), ctx: ctx, cancel: cancel, SendWoL: wol.Send, VNC: macvnc.Run, Probe: probe.TCP}
	s.desired = cfg
	s.removeJobFile = os.Remove
	if err = s.load(); err != nil {
		cancel()
		return nil, err
	}
	for i := 0; i < cfg.MaxConcurrent; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	return s, nil
}
func (s *Service) load() error {
	if err := s.fs.Read("devices.json", &s.devices); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.devices.SchemaVersion != model.Schema || s.devices.Devices == nil || len(s.devices.Devices) > 1000 {
		return fmt.Errorf("设备文件格式无效")
	}
	seen := map[string]bool{}
	for i := range s.devices.Devices {
		d := &s.devices.Devices[i]
		d.Defaults()
		if seen[d.ID] {
			return fmt.Errorf("设备 ID 重复")
		}
		seen[d.ID] = true
		if e := d.Validate(s.cfg); e != nil {
			return fmt.Errorf("设备 %s: %w", d.ID, e)
		}
	}
	if e := s.creds.Prune(seen); e != nil {
		return e
	}
	if err := s.fs.Read("runtime-policy.json", &s.policy); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.policy.SchemaVersion != model.Schema || s.policy.Targets == nil {
		return fmt.Errorf("认证保护文件格式无效")
	}
	for k, g := range s.policy.Targets {
		if g.InFlight {
			g.InFlight = false
			g.Until = time.Now().Add(time.Minute)
			s.policy.Targets[k] = g
		}
	}
	if err := s.fs.Write("runtime-policy.json", s.policy); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.fs.Path("jobs"))
	if err != nil {
		return err
	}
	if len(entries) > s.cfg.MaxJobs+128 {
		return fmt.Errorf("任务目录记录过多，请离线归档")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var j model.Job
		if err = s.fs.Read(filepath.Join("jobs", e.Name()), &j); err != nil {
			return err
		}
		if j.SchemaVersion != model.Schema || !model.ValidID(j.ID) || !model.ValidID(j.CommandID) || e.Name() != j.ID+".json" {
			return fmt.Errorf("任务记录无效")
		}
		if _, ok := s.commands[j.CommandID]; ok {
			return fmt.Errorf("任务 command_id 重复")
		}
		if j.Active() {
			j.Status = "interrupted"
			j.Code = "process_interrupted"
			j.Message = "上次执行结果不明，不会自动重试"
			j.FinishedAt = time.Now().UTC()
			if err = s.fs.Write(filepath.Join("jobs", e.Name()), j); err != nil {
				return err
			}
		}
		s.jobs[j.ID] = &j
		s.commands[j.CommandID] = j.ID
	}
	if err := s.cleanupLocked(); err != nil {
		return err
	}
	s.rebuildJobOrderLocked()
	return nil
}
func (s *Service) Config() model.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.AllowedNetworks = append([]string{}, c.AllowedNetworks...)
	return c
}
func (s *Service) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.storageErr == nil
}

// Settings includes saved values which only become effective after a restart.
func (s *Service) Settings() model.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.desired
	c.AllowedNetworks = append([]string{}, c.AllowedNetworks...)
	return c
}
func (s *Service) storageHealthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storageErr == nil
}
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeStreamsLocked()
	s.cancel()
	for _, c := range s.cancels {
		c()
	}
	close(s.queue)
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) write(name string, v any) error {
	if e := s.fs.Write(name, v); e != nil {
		s.storageFailedLocked(e)
		slog.Error("持久化失败，暂停写入和新任务", "file", name)
		return model.Error(503, "storage_unavailable", "存储不可用，请检查磁盘后重启")
	}
	return nil
}
func (s *Service) writable() error {
	if s.closed || s.storageErr != nil {
		return model.Error(503, "storage_unavailable", "服务停止或存储不可用")
	}
	return nil
}
func (s *Service) device(id string) (model.Device, bool) {
	for _, d := range s.devices.Devices {
		if d.ID == id {
			return d, true
		}
	}
	return model.Device{}, false
}
func (s *Service) Devices() model.DeviceFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.devices
	out.Devices = append([]model.Device{}, s.devices.Devices...)
	for i := range out.Devices {
		d := &out.Devices[i]
		d.PasswordConfigured = s.creds.Has(*d)
		d.ActiveJobID = s.active[d.ID]
	}
	return out
}
func (s *Service) SaveDevice(d model.Device, password *string, clearPassword bool, create bool) (model.Device, error) {
	return s.saveDevice(d, password, clearPassword, create, false)
}

func (s *Service) saveDevice(d model.Device, password *string, clearPassword bool, create, replayCreate bool) (model.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return d, e
	}
	// IDs are internal identities, independent of a device's editable name.
	// Keep accepting explicit IDs for existing API clients and imported data.
	if create && d.ID == "" {
		for {
			d.ID = model.ID("dev_")
			if _, exists := s.device(d.ID); !exists {
				break
			}
		}
	}
	if create {
		if d.WoL.Broadcast == "" {
			d.WoL.Broadcast = s.cfg.DeviceDefaults.Broadcast
		}
		if d.WoL.Port == 0 {
			d.WoL.Port = s.cfg.DeviceDefaults.Port
		}
		if d.WoL.Repeat == 0 {
			d.WoL.Repeat = s.cfg.DeviceDefaults.Repeat
		}
	}
	d.Defaults()
	if e := d.Validate(s.cfg); e != nil {
		return d, model.Error(422, "invalid_device", e.Error())
	}
	old, exists := s.device(d.ID)
	if create && exists {
		if replayCreate {
			return s.replayDeviceCreationLocked(d, old, password, clearPassword)
		}
		return d, model.Error(409, "device_exists", "设备 ID 已存在")
	}
	if !create && !exists {
		return d, model.Error(404, "device_not_found", "设备不存在")
	}
	if exists && old.Revision != d.Revision {
		return d, model.Error(409, "revision_conflict", "设备已修改，请刷新后重试")
	}
	if s.active[d.ID] != "" {
		return d, model.Error(409, "device_busy", "任务执行期间不能修改设备")
	}
	if create && len(s.devices.Devices) >= 1000 {
		return d, model.Error(422, "device_limit", "最多保存 1000 台设备")
	}
	if password != nil && clearPassword {
		return d, model.Error(422, "invalid_credentials", "不能同时设置和清除密码")
	}
	if password != nil && !model.CredentialText(*password) {
		return d, model.Error(422, "invalid_credentials", "密码须为 1–63 UTF-8 字节，不能包含 NUL 或换行")
	}
	if exists && credentials.Binding(old) != credentials.Binding(d) && s.creds.Has(old) && password == nil && !clearPassword {
		return d, model.Error(409, "credentials_rebind_required", "目标或账户已变更，请重新设置或清除密码")
	}
	if password != nil {
		if e := s.creds.Set(d, *password); e != nil {
			s.storageFailedLocked(e)
			return d, model.Error(503, "credentials_write_failed", "无法保存密码")
		}
	}
	if clearPassword {
		if e := s.creds.Delete(d.ID); e != nil {
			s.storageFailedLocked(e)
			return d, model.Error(503, "credentials_write_failed", "无法清除密码")
		}
	}
	d.Revision = old.Revision + 1
	d.PasswordConfigured = false
	d.ActiveJobID = ""
	next := s.devices
	next.Revision++
	next.Devices = append([]model.Device{}, s.devices.Devices...)
	if exists {
		for i := range next.Devices {
			if next.Devices[i].ID == d.ID {
				next.Devices[i] = d
			}
		}
	} else {
		next.Devices = append(next.Devices, d)
	}
	if e := s.write("devices.json", next); e != nil {
		return d, e
	}
	s.devices = next
	d.PasswordConfigured = s.creds.Has(d)
	s.devicesEventLocked()
	return d, nil
}
func (s *Service) DeleteDevice(id string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return e
	}
	d, ok := s.device(id)
	if !ok {
		return model.Error(404, "device_not_found", "设备不存在")
	}
	if d.Revision != revision {
		return model.Error(409, "revision_conflict", "设备已修改，请刷新")
	}
	if s.active[id] != "" {
		return model.Error(409, "device_busy", "任务执行期间不能删除设备")
	}
	next := s.devices
	next.Revision++
	next.Devices = []model.Device{}
	for _, v := range s.devices.Devices {
		if v.ID != id {
			next.Devices = append(next.Devices, v)
		}
	}
	if e := s.write("devices.json", next); e != nil {
		return e
	}
	s.devices = next
	if e := s.creds.Delete(id); e != nil {
		s.devicesEventLocked()
		s.storageFailedLocked(e)
		return model.Error(503, "credentials_write_failed", "设备已删除，凭据清理失败，请检查存储")
	}
	s.devicesEventLocked()
	return nil
}

// Import is add-only. Preview never changes devices or credentials.
func (s *Service) Import(devices []model.Device, revision int, commit bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return e
	}
	if len(devices) == 0 || len(devices)+len(s.devices.Devices) > 1000 {
		return model.Error(422, "device_limit", "导入数量须为 1–1000 且总数不超过 1000")
	}
	if commit && revision != s.devices.Revision {
		return model.Error(409, "revision_conflict", "设备列表已变更，请重新预览")
	}
	seen := map[string]bool{}
	next := s.devices
	next.Devices = append([]model.Device{}, s.devices.Devices...)
	for _, d := range next.Devices {
		seen[d.ID] = true
	}
	for _, d := range devices {
		d.Defaults()
		if e := d.Validate(s.cfg); e != nil {
			return model.Error(422, "invalid_device", e.Error())
		}
		if seen[d.ID] {
			return model.Error(409, "device_exists", "导入设备 ID 与现有或导入列表重复")
		}
		seen[d.ID] = true
		d.Revision = 1
		d.PasswordConfigured = false
		d.ActiveJobID = ""
		next.Devices = append(next.Devices, d)
	}
	if !commit {
		return nil
	}
	next.Revision++
	if e := s.write("devices.json", next); e != nil {
		return e
	}
	s.devices = next
	s.devicesEventLocked()
	return nil
}
func (s *Service) SetCredential(id string, password *string, revision int) error { // Reuses revision, busy and binding checks.
	s.mu.Lock()
	d, ok := s.device(id)
	s.mu.Unlock()
	if !ok {
		return model.Error(404, "device_not_found", "设备不存在")
	}
	d.Revision = revision
	_, e := s.SaveDevice(d, password, password == nil, false)
	return e
}
func (s *Service) UpdateSettings(c model.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return e
	}
	if c.DeviceDefaults == (model.DeviceDefaults{}) {
		c.DeviceDefaults = s.desired.DeviceDefaults
	}
	if e := c.Validate(); e != nil {
		return model.Error(422, "invalid_settings", e.Error())
	}
	if c.GatewayID != s.cfg.GatewayID {
		return model.Error(422, "immutable_id", "不能修改 Gateway ID")
	}
	if len(s.active) > 0 {
		return model.Error(409, "device_busy", "有活动任务时不能修改设置")
	}
	if c.MaxJobs < len(s.jobs) {
		return model.Error(422, "job_limit_below_usage", fmt.Sprintf("当前有 %d 条任务记录，请先清理，或将上限设置为不小于当前数量", len(s.jobs)))
	}
	for _, d := range s.devices.Devices {
		if e := d.Validate(c); e != nil {
			return model.Error(422, "invalid_settings", "新的网段设置不允许现有设备")
		}
	}
	// Worker count and listener changes take effect only after restart.
	if e := s.write("config.json", c); e != nil {
		return e
	}
	s.desired = c
	s.cfg.Name = c.Name
	s.cfg.AllowedNetworks = c.AllowedNetworks
	s.cfg.DeviceDefaults = c.DeviceDefaults
	s.cfg.MaxJobs = c.MaxJobs
	return nil
}
func (s *Service) Submit(ctx context.Context, cmd model.Command) (model.Job, error) {
	// Expired-file IO runs outside the application lock. Do not expire a known
	// command as a side effect of its own idempotent retry.
	s.mu.Lock()
	known := s.commands[cmd.CommandID] != ""
	s.mu.Unlock()
	if !known {
		if err := s.cleanupExpired(ctx); err != nil {
			return model.Job{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return model.Job{}, e
	}
	if e := ctx.Err(); e != nil {
		return model.Job{}, e
	}
	if !model.ValidID(cmd.CommandID) || (cmd.Action != "wake" && cmd.Action != "probe") {
		return model.Job{}, model.Error(422, "invalid_command", "无效的命令 ID 或操作")
	}
	if id, ok := s.commands[cmd.CommandID]; ok {
		j := s.jobs[id]
		if j.DeviceID != cmd.DeviceID || j.Action != cmd.Action {
			return model.Job{}, model.Error(409, "idempotency_conflict", "相同命令 ID 的参数不同")
		}
		return j.Clone(), nil
	}
	now := time.Now().UTC()
	if cmd.SchemaVersion != model.Schema || cmd.IssuedAt.After(now.Add(5*time.Second)) || cmd.ExpiresAt.Sub(cmd.IssuedAt) > time.Minute || !cmd.ExpiresAt.After(cmd.IssuedAt) {
		return model.Job{}, model.Error(422, "invalid_command_time", "命令有效期必须在 60 秒内")
	}
	if !now.Before(cmd.ExpiresAt) {
		return model.Job{}, model.Error(422, "command_expired", "命令已过期")
	}
	d, ok := s.device(cmd.DeviceID)
	if !ok {
		return model.Job{}, model.Error(404, "device_not_found", "设备不存在")
	}
	if !d.Enabled {
		return model.Job{}, model.Error(422, "device_disabled", "设备已禁用")
	}
	if e := d.Validate(s.cfg); e != nil {
		return model.Job{}, model.Error(422, "invalid_device", e.Error())
	}
	if id := s.active[d.ID]; id != "" {
		return model.Job{}, &model.Fault{Status: 409, Code: "device_busy", Message: "设备已有任务", JobID: id}
	}
	if cmd.Action == "probe" {
		if d.IP == "" || (!d.HasVNC() && !d.Probe.Enabled) {
			return model.Job{}, model.Error(422, "probe_not_configured", "请配置目标 IP 和检测端口")
		}
	} else {
		if d.WakeMethod != "mac_vnc" {
			if _, e := wol.Source(d.WoL); e != nil {
				return model.Job{}, model.Error(422, "invalid_interface", e.Error())
			}
		}
		if d.HasVNC() {
			if !s.creds.Has(d) {
				return model.Job{}, model.Error(422, "credentials_required", "请为当前目标配置 VNC 密码")
			}
			p, e := s.creds.Get(d)
			clear(p)
			if e != nil {
				return model.Job{}, model.Error(422, "credentials_unavailable", e.Error())
			}
			if id := s.targets[d.TargetKey()]; id != "" {
				return model.Job{}, &model.Fault{Status: 409, Code: "device_busy", Message: "同一 VNC 目标已有任务", JobID: id}
			}
			g := s.policy.Targets[d.TargetKey()]
			if g.Blocked || g.InFlight || g.Until.After(now) {
				return model.Job{}, model.Error(429, "authentication_cooldown", "VNC 认证保护中，请等待或检查账户后解除保护")
			}
		}
	}
	if len(s.queue) >= cap(s.queue) {
		return model.Job{}, model.Error(503, "queue_full", "任务队列已满")
	}
	if len(s.jobs) >= s.cfg.MaxJobs {
		return model.Job{}, model.Error(503, "job_limit", "任务存储已满，请归档历史")
	}
	j := model.Job{SchemaVersion: model.Schema, ID: model.ID("job_"), CommandID: cmd.CommandID, DeviceID: d.ID, Action: cmd.Action, DeviceRevision: d.Revision, Status: "pending", Phase: "queued", CreatedAt: now, ExpiresAt: cmd.ExpiresAt, Steps: []model.Step{}, Observation: model.Observation{State: "unknown"}}
	if e := s.write(filepath.Join("jobs", j.ID+".json"), j); e != nil {
		return model.Job{}, e
	}
	s.jobs[j.ID] = &j
	s.indexJobLocked(j)
	s.commands[cmd.CommandID] = j.ID
	s.active[d.ID] = j.ID
	if d.HasVNC() && cmd.Action == "wake" {
		s.targets[d.TargetKey()] = j.ID
	}
	taskCtx, cancel := context.WithCancel(s.ctx)
	s.cancels[j.ID] = cancel
	s.queue <- task{id: j.ID, device: d, ctx: taskCtx, cancel: cancel}
	s.jobEventLocked(&j)
	return j.Clone(), nil
}
func (s *Service) Get(id string) (model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return model.Job{}, model.Error(404, "job_not_found", "任务不存在或已超出保留期")
	}
	return j.Clone(), nil
}
func (s *Service) Jobs() []model.Job {
	s.mu.Lock()
	out := make([]model.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.Clone())
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return keyBefore(jobKey{out[i].ID, out[i].CreatedAt}, jobKey{out[j].ID, out[j].CreatedAt})
	})
	return out
}
func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return model.Error(404, "job_not_found", "任务不存在")
	}
	if c := s.cancels[id]; c != nil && j.Active() {
		c()
	}
	return nil
}
func (s *Service) ClearGuard(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return e
	}
	d, ok := s.device(id)
	if !ok {
		return model.Error(404, "device_not_found", "设备不存在")
	}
	if s.targets[d.TargetKey()] != "" {
		return model.Error(409, "device_busy", "认证任务进行中")
	}
	delete(s.policy.Targets, d.TargetKey())
	return s.write("runtime-policy.json", s.policy)
}
func (s *Service) cleanupLocked() error {
	cutoff := time.Now().Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)
	for id, j := range s.jobs {
		if !j.Active() && j.FinishedAt.Before(cutoff) && j.ExpiresAt.Before(time.Now().Add(-time.Minute)) {
			if e := os.Remove(s.fs.Path(filepath.Join("jobs", id+".json"))); e != nil && !errors.Is(e, os.ErrNotExist) {
				s.storageFailedLocked(e)
				return model.Error(503, "storage_unavailable", "无法清理过期记录")
			}
			delete(s.commands, j.CommandID)
			delete(s.jobs, id)
		}
	}
	return nil
}

func (s *Service) worker() {
	defer s.wg.Done()
	for t := range s.queue {
		s.run(t)
		t.cancel()
	}
}
func (s *Service) run(t task) {
	if t.device.HasVNC() {
		select {
		case s.vncSlots <- struct{}{}:
			defer func() { <-s.vncSlots }()
		case <-t.ctx.Done():
			s.finish(t, "cancelled", "cancelled", "任务已取消")
			return
		}
	}
	s.mu.Lock()
	j := s.jobs[t.id]
	if e := t.ctx.Err(); e != nil {
		s.mu.Unlock()
		s.finish(t, "cancelled", "cancelled", "任务已取消")
		return
	}
	if s.storageErr != nil {
		s.mu.Unlock()
		return
	}
	if !time.Now().Before(j.ExpiresAt) {
		s.mu.Unlock()
		s.finish(t, "expired", "command_expired", "排队期间命令已过期")
		return
	}
	j.Status = "running"
	j.Phase = "starting"
	j.StartedAt = time.Now().UTC()
	if e := s.write(filepath.Join("jobs", j.ID+".json"), j); e != nil {
		s.mu.Unlock()
		return
	}
	action := j.Action
	s.jobEventLocked(j)
	s.mu.Unlock()
	budget := 30 * time.Second
	if action == "probe" {
		budget = 5 * time.Second
	} else if t.device.WakeMethod == "wol" {
		budget = 5 * time.Second
		if t.device.Probe.Enabled {
			budget += time.Duration(t.device.Probe.TimeoutSeconds) * time.Second
		}
	} else {
		budget += time.Duration(t.device.VNC.HoldSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(t.ctx, budget)
	defer cancel()
	authDeadline := time.Now().Add(30 * time.Second)
	success, failed := 0, 0
	timedOut := false
	execute := func(name string, fn func() error) error {
		s.stepStart(t.id, name)
		if e := ctx.Err(); e != nil {
			return e
		}
		if !s.storageHealthy() {
			return model.Error(503, "storage_unavailable", "存储不可用")
		}
		e := fn()
		if errors.Is(e, context.DeadlineExceeded) {
			timedOut = true
		}
		s.stepEnd(t.id, name, e)
		if e == nil {
			success++
		} else {
			failed++
		}
		return e
	}
	if action == "probe" {
		port := t.device.Probe.Port
		if t.device.HasVNC() {
			port = t.device.VNC.Port
		}
		e := execute("probe", func() error { return s.Probe(ctx, t.device.IP, port) })
		if e == nil {
			s.observe(t.id, "reachable", "tcp")
		}
	} else {
		if t.device.WakeMethod != "mac_vnc" {
			_ = execute("wol", func() error { return s.SendWoL(ctx, t.device.WoL) })
		}
		if t.device.HasVNC() && ctx.Err() == nil && s.storageHealthy() {
			_ = execute("vnc", func() error { return s.runVNC(ctx, t, authDeadline) })
		} else if t.device.WakeMethod == "wol" && t.device.Probe.Enabled && success > 0 && ctx.Err() == nil {
			_ = execute("probe", func() error {
				pctx, pcancel := context.WithTimeout(ctx, time.Duration(t.device.Probe.TimeoutSeconds)*time.Second)
				defer pcancel()
				for {
					if e := s.Probe(pctx, t.device.IP, t.device.Probe.Port); e == nil {
						s.observe(t.id, "reachable", "tcp")
						return nil
					}
					select {
					case <-pctx.Done():
						return pctx.Err()
					case <-time.After(2 * time.Second):
					}
				}
			})
		}
	}
	if !s.storageHealthy() {
		return
	}
	status, code, message := "success", "completed", "唤醒流程已完成；请查看各步骤确认结果"
	if action == "probe" {
		message = "TCP 检测完成"
	}
	if failed > 0 {
		status = "failed"
		code = "steps_failed"
		message = "操作未完成，请查看步骤结果"
		if success > 0 {
			status = "partial"
			message = "部分步骤完成，请查看步骤结果"
		}
	}
	if e := ctx.Err(); e != nil || timedOut {
		status = "timeout"
		code = "timeout"
		message = "执行超时，已完成的步骤仍然有效"
		if errors.Is(e, context.Canceled) {
			status = "cancelled"
			code = "cancelled"
			message = "任务已取消，已发送的请求无法撤销"
		}
	}
	s.finish(t, status, code, message)
}
func (s *Service) runVNC(ctx context.Context, t task, deadline time.Time) error {
	s.mu.Lock()
	password, e := s.creds.Get(t.device)
	s.mu.Unlock()
	if e != nil {
		return e
	}
	defer clear(password)
	ev, e := s.VNC(ctx, macvnc.Options{IP: t.device.IP, Port: t.device.VNC.Port, Username: t.device.VNC.Username, Password: password, Hold: time.Duration(t.device.VNC.HoldSeconds) * time.Second, AuthDeadline: deadline, Progress: func(p string) { s.phase(t.id, p) }, BeforeAuth: func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.writable(); e != nil {
			return e
		}
		s.policy.Targets[t.device.TargetKey()] = Guard{InFlight: true, Until: time.Now().Add(time.Minute)}
		return s.write("runtime-policy.json", s.policy)
	}})
	if ev.Connected {
		s.observe(t.id, "reachable", "tcp")
	}
	if ev.Authenticated {
		s.observe(t.id, "service_ready", "vnc_auth")
	}
	if ev.AuthSent {
		s.mu.Lock()
		if ev.Authenticated {
			delete(s.policy.Targets, t.device.TargetKey())
		} else {
			s.policy.Targets[t.device.TargetKey()] = Guard{Until: time.Now().Add(time.Minute), Blocked: macvnc.Code(e) == "auth_limited"}
		}
		pe := s.write("runtime-policy.json", s.policy)
		s.mu.Unlock()
		if pe != nil {
			return pe
		}
	}
	return e
}
func (s *Service) phase(id, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[id].Phase = p
	s.jobEventLocked(s.jobs[id])
}
func (s *Service) observe(id, state, kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[id].Observation = model.Observation{State: state, ProbeType: kind, ObservedAt: time.Now().UTC()}
	s.jobEventLocked(s.jobs[id])
}
func (s *Service) stepStart(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	j.Phase = name
	j.Steps = append(j.Steps, model.Step{Name: name, Status: "running", StartedAt: time.Now().UTC()})
	s.jobEventLocked(j)
}
func (s *Service) stepEnd(id, name string, e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	defer s.jobEventLocked(j)
	step := &j.Steps[len(j.Steps)-1]
	step.FinishedAt = time.Now().UTC()
	step.Status = "success"
	step.Code = "completed"
	switch name {
	case "wol":
		step.Code = "packet_sent"
		step.Message = "Magic Packet 已发送，不代表目标已开机"
	case "probe":
		step.Code = "tcp_reachable"
		step.Message = "目标 TCP 端口可达"
	case "vnc":
		step.Code = "vnc_authenticated"
		step.Message = "VNC 认证及保持完成，不代表物理屏幕已亮"
	}
	if e != nil {
		step.Status = "failed"
		step.Code = "network_failed"
		step.Message = "网络操作失败，请检查设备和网络配置"
		if name == "vnc" {
			step.Code = macvnc.Code(e)
			step.Message = macvnc.Message(step.Code)
		}
		if errors.Is(e, context.DeadlineExceeded) {
			step.Code = "timeout"
			step.Message = "执行超时"
		}
		if errors.Is(e, context.Canceled) {
			step.Code = "cancelled"
			step.Message = "操作已取消"
		}
	}
}
func (s *Service) finish(t task, status, code, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[t.id]
	j.Status = status
	j.Phase = "finished"
	j.Code = code
	j.Message = msg
	j.FinishedAt = time.Now().UTC()
	if e := s.write(filepath.Join("jobs", j.ID+".json"), j); e != nil {
		j.Status = "running"
		j.Code = "storage_unavailable"
		j.Message = "结果无法保存，设备已锁定，请检查存储后重启"
		s.jobEventLocked(j)
		return
	}
	delete(s.active, t.device.ID)
	if t.device.HasVNC() && s.targets[t.device.TargetKey()] == t.id {
		delete(s.targets, t.device.TargetKey())
	}
	delete(s.cancels, t.id)
	s.jobEventLocked(j)
}
