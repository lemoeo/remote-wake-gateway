package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const Schema = 1

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func ID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func ValidID(s string) bool { return validID.MatchString(s) }

type Config struct {
	SchemaVersion   int            `json:"schema_version"`
	GatewayID       string         `json:"gateway_id"`
	Name            string         `json:"name"`
	Listen          string         `json:"listen"`
	PublicURL       string         `json:"public_url"`
	AllowedNetworks []string       `json:"allowed_networks"`
	MaxConcurrent   int            `json:"max_concurrent"`
	MaxVNC          int            `json:"max_vnc"`
	QueueSize       int            `json:"queue_size"`
	RetentionDays   int            `json:"retention_days"`
	MaxJobs         int            `json:"max_jobs"`
	DeviceDefaults  DeviceDefaults `json:"device_defaults"`
}

type DeviceDefaults struct {
	Broadcast string `json:"broadcast"`
	Port      int    `json:"port"`
	Repeat    int    `json:"repeat"`
}

func DefaultDeviceDefaults() DeviceDefaults {
	return DeviceDefaults{Broadcast: "255.255.255.255", Port: 9, Repeat: 3}
}

// Older configuration files omit this section. Normalize the whole missing
// section, while still rejecting partially supplied invalid defaults.
func (c *Config) Defaults() {
	if c.DeviceDefaults == (DeviceDefaults{}) {
		c.DeviceDefaults = DefaultDeviceDefaults()
	}
}

func DefaultConfig() Config {
	return Config{SchemaVersion: Schema, GatewayID: ID("gw_"), Name: "Home Gateway", Listen: "127.0.0.1:8080", AllowedNetworks: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, MaxConcurrent: 4, MaxVNC: 2, QueueSize: 16, RetentionDays: 7, MaxJobs: 10000, DeviceDefaults: DefaultDeviceDefaults()}
}
func (c Config) Validate() error {
	c.Defaults()
	if c.SchemaVersion != Schema || !ValidID(c.GatewayID) {
		return fmt.Errorf("不支持的配置版本或 Gateway ID")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("无效的监听地址")
	}
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("public_url 必须是完整 HTTP(S) 源地址")
		}
	}
	if len(c.Name) < 1 || len(c.Name) > 100 || c.MaxConcurrent < 1 || c.MaxConcurrent > 16 || c.MaxVNC < 1 || c.MaxVNC > c.MaxConcurrent || c.QueueSize < 1 || c.QueueSize > 128 || c.MaxJobs < 100 || c.MaxJobs > 100000 || c.RetentionDays < 1 || c.RetentionDays > 90 {
		return fmt.Errorf("配置值超出范围")
	}
	if len(c.AllowedNetworks) == 0 || len(c.AllowedNetworks) > 64 {
		return fmt.Errorf("需要配置允许访问的目标网段")
	}
	for _, s := range c.AllowedNetworks {
		if _, err := netip.ParsePrefix(s); err != nil {
			return fmt.Errorf("无效的允许网段")
		}
	}
	if !c.AllowsWoL(c.DeviceDefaults.Broadcast) || c.DeviceDefaults.Port < 1 || c.DeviceDefaults.Port > 65535 || c.DeviceDefaults.Repeat < 1 || c.DeviceDefaults.Repeat > 10 {
		return fmt.Errorf("新设备默认广播地址、UDP 端口或发送次数无效")
	}
	return nil
}
func (c Config) AllowsWoL(target string) bool {
	a, e := netip.ParseAddr(target)
	return e == nil && a.Is4() && !a.IsUnspecified() && !a.IsMulticast() && (target == "255.255.255.255" || c.Allows(target))
}
func (c Config) Allows(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, s := range c.AllowedNetworks {
		p, e := netip.ParsePrefix(s)
		if e == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

type WoL struct {
	MAC        string `json:"mac"`
	Broadcast  string `json:"broadcast"`
	Interface  string `json:"interface"`
	Port       int    `json:"port"`
	Repeat     int    `json:"repeat"`
	IntervalMS int    `json:"interval_ms"`
}
type VNC struct {
	Port        int    `json:"port"`
	Username    string `json:"username"`
	HoldSeconds int    `json:"hold_seconds"`
}
type Probe struct {
	Enabled        bool `json:"enabled"`
	Port           int  `json:"port"`
	TimeoutSeconds int  `json:"timeout_seconds"`
}
type Device struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Type               string `json:"type"`
	Enabled            bool   `json:"enabled"`
	Revision           int    `json:"revision"`
	WakeMethod         string `json:"wake_method"`
	IP                 string `json:"ip"`
	WoL                WoL    `json:"wol"`
	VNC                VNC    `json:"vnc"`
	Probe              Probe  `json:"probe"`
	Note               string `json:"note"`
	PasswordConfigured bool   `json:"password_configured,omitempty"`
	ActiveJobID        string `json:"active_job_id,omitempty"`
}

func (d Device) HasVNC() bool { return d.WakeMethod == "mac_vnc" || d.WakeMethod == "wol_mac_vnc" }
func (d Device) TargetKey() string {
	if !d.HasVNC() {
		return ""
	}
	return d.IP + ":" + fmt.Sprint(d.VNC.Port) + ":" + d.VNC.Username
}
func (d *Device) Defaults() {
	if d.WakeMethod == "" {
		d.WakeMethod = "wol"
	}
	if d.WoL.Port == 0 {
		d.WoL.Port = 9
	}
	if d.WoL.Repeat == 0 {
		d.WoL.Repeat = 3
	}
	if d.WoL.IntervalMS == 0 {
		d.WoL.IntervalMS = 100
	}
	if d.VNC.Port == 0 {
		d.VNC.Port = 5900
	}
	if d.Probe.TimeoutSeconds == 0 {
		d.Probe.TimeoutSeconds = 60
	}
}
func CredentialText(s string) bool {
	return len(s) > 0 && len(s) <= 63 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func (d Device) Validate(c Config) error {
	if !ValidID(d.ID) || strings.TrimSpace(d.Name) == "" || len(d.Name) > 100 || len(d.Note) > 2000 {
		return fmt.Errorf("设备 ID、名称或备注无效")
	}
	if d.WakeMethod != "wol" && !d.HasVNC() {
		return fmt.Errorf("不支持的唤醒方式")
	}
	if d.IP != "" {
		a, e := netip.ParseAddr(d.IP)
		if e != nil || !a.Is4() || a.IsMulticast() || a.IsUnspecified() || !c.Allows(d.IP) {
			return fmt.Errorf("目标必须是允许网段内的 IPv4 地址")
		}
	}
	if d.WakeMethod != "mac_vnc" {
		mac, e := net.ParseMAC(d.WoL.MAC)
		if e != nil || len(mac) != 6 || mac[0]&1 != 0 {
			return fmt.Errorf("需要有效的单播 MAC 地址")
		}
		if !c.AllowsWoL(d.WoL.Broadcast) || d.WoL.Port < 1 || d.WoL.Port > 65535 || d.WoL.Repeat < 1 || d.WoL.Repeat > 10 || d.WoL.IntervalMS < 50 || d.WoL.IntervalMS > 1000 {
			return fmt.Errorf("WoL 配置无效")
		}
	}
	if d.HasVNC() && (d.IP == "" || d.VNC.Port < 1 || d.VNC.Port > 65535 || !CredentialText(d.VNC.Username) || d.VNC.HoldSeconds < 0 || d.VNC.HoldSeconds > 30) {
		return fmt.Errorf("VNC 配置无效")
	}
	if d.Probe.Enabled && (d.IP == "" || d.Probe.Port < 1 || d.Probe.Port > 65535 || d.Probe.TimeoutSeconds < 1 || d.Probe.TimeoutSeconds > 60) {
		return fmt.Errorf("TCP 检测配置无效")
	}
	return nil
}

type DeviceFile struct {
	SchemaVersion int      `json:"schema_version"`
	Revision      int      `json:"revision"`
	Devices       []Device `json:"devices"`
}
type Command struct {
	SchemaVersion int       `json:"schema_version"`
	CommandID     string    `json:"command_id"`
	Action        string    `json:"action"`
	DeviceID      string    `json:"device_id"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}
type Step struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Code       string    `json:"code"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}
type Observation struct {
	State      string    `json:"state"`
	ProbeType  string    `json:"probe_type,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}
type Job struct {
	SchemaVersion  int         `json:"schema_version"`
	ID             string      `json:"job_id"`
	CommandID      string      `json:"command_id"`
	DeviceID       string      `json:"device_id"`
	Action         string      `json:"action"`
	DeviceRevision int         `json:"device_revision"`
	Status         string      `json:"status"`
	Phase          string      `json:"phase"`
	Code           string      `json:"code,omitempty"`
	Message        string      `json:"message,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	ExpiresAt      time.Time   `json:"expires_at"`
	StartedAt      time.Time   `json:"started_at,omitempty"`
	FinishedAt     time.Time   `json:"finished_at,omitempty"`
	Steps          []Step      `json:"steps"`
	Observation    Observation `json:"observation"`
}

func (j Job) Active() bool { return j.Status == "pending" || j.Status == "running" }
func (j Job) Clone() Job   { j.Steps = append([]Step{}, j.Steps...); return j }

type Fault struct {
	Status  int
	Code    string
	Message string
	JobID   string
}

func (e *Fault) Error() string { return e.Message }
func Error(status int, code, msg string) error {
	return &Fault{Status: status, Code: code, Message: msg}
}
