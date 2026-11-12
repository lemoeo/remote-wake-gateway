package httptransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/app"
	"github.com/lemoeo/remote-wake-gateway/internal/auth"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	webui "github.com/lemoeo/remote-wake-gateway/internal/web"
	"github.com/lemoeo/remote-wake-gateway/internal/wol"
)

type Server struct {
	App             *app.Service
	Auth            *auth.Auth
	Version         string
	started         time.Time
	publicURL       *url.URL
	handler         http.Handler
	streamHeartbeat time.Duration
}

func New(service *app.Service, a *auth.Auth, version string) (*Server, error) {
	s := &Server{App: service, Auth: a, Version: version, started: time.Now(), streamHeartbeat: 15 * time.Second}
	if raw := service.Config().PublicURL; raw != "" {
		u, e := url.Parse(raw)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
			return nil, fmt.Errorf("public_url 必须是完整 HTTP(S) 源地址")
		}
		s.publicURL = u
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"alive": true}) })
	mux.HandleFunc("GET /api/v1/readyz", func(w http.ResponseWriter, r *http.Request) {
		status := 200
		if !s.App.Ready() {
			status = 503
		}
		reply(w, status, map[string]bool{"ready": status == 200})
	})
	mux.HandleFunc("GET /api/v1/auth/session", s.session)
	mux.HandleFunc("POST /api/v1/auth/setup", s.setup)
	mux.HandleFunc("POST /api/v1/auth/session", s.login)
	mux.HandleFunc("DELETE /api/v1/auth/session", s.protect(s.logout))
	mux.HandleFunc("GET /api/v1/gateway", s.protect(func(w http.ResponseWriter, r *http.Request) {
		c := s.App.Config()
		reply(w, 200, map[string]any{"gateway_id": c.GatewayID, "name": c.Name, "version": s.Version, "uptime_seconds": int(time.Since(s.started).Seconds()), "capabilities": []string{"wol", "mac_vnc", "wol_mac_vnc", "tcp_probe"}})
	}))
	mux.HandleFunc("GET /api/v1/settings", s.protect(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, s.App.Settings()) }))
	mux.HandleFunc("PUT /api/v1/settings", s.protect(s.settings))
	mux.HandleFunc("GET /api/v1/network/interfaces", s.protect(func(w http.ResponseWriter, r *http.Request) {
		v, e := wol.Interfaces()
		if e != nil {
			writeError(w, e)
			return
		}
		reply(w, 200, v)
	}))
	mux.HandleFunc("GET /api/v1/devices", s.protect(func(w http.ResponseWriter, r *http.Request) { reply(w, 200, s.App.Devices()) }))
	mux.HandleFunc("GET /api/v1/events", s.protect(s.events))
	mux.HandleFunc("POST /api/v1/devices", s.protect(s.saveDevice))
	mux.HandleFunc("PUT /api/v1/devices/{id}", s.protect(s.saveDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.protect(s.deleteDevice))
	mux.HandleFunc("PUT /api/v1/devices/{id}/credentials/vnc", s.protect(s.credential))
	mux.HandleFunc("DELETE /api/v1/devices/{id}/credentials/vnc", s.protect(s.credential))
	mux.HandleFunc("POST /api/v1/devices/{id}/authentication/reset", s.protect(func(w http.ResponseWriter, r *http.Request) {
		if e := s.App.ClearGuard(r.PathValue("id")); e != nil {
			writeError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"cleared": true})
	}))
	mux.HandleFunc("POST /api/v1/devices/{id}/wake", s.protect(s.submit))
	mux.HandleFunc("POST /api/v1/devices/{id}/probe", s.protect(s.submit))
	mux.HandleFunc("GET /api/v1/wake-jobs", s.protect(s.jobs))
	mux.HandleFunc("GET /api/v1/wake-jobs/cleanup", s.protect(s.previewHistoryCleanup))
	mux.HandleFunc("POST /api/v1/wake-jobs/cleanup", s.protect(s.cleanupHistory))
	mux.HandleFunc("GET /api/v1/wake-jobs/{id}", s.protect(s.getJob))
	mux.HandleFunc("POST /api/v1/wake-jobs/{id}/cancel", s.protect(func(w http.ResponseWriter, r *http.Request) {
		if e := s.App.Cancel(r.PathValue("id")); e != nil {
			writeError(w, e)
			return
		}
		reply(w, 202, map[string]bool{"cancel_requested": true})
	}))
	mux.HandleFunc("GET /api/v1/devices/export", s.protect(s.export))
	mux.HandleFunc("POST /api/v1/devices/import", s.protect(s.importDevices))
	mux.Handle("/", webui.Handler())
	s.handler = s.middleware(mux)
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		defer func() {
			if recover() != nil {
				slog.Error("HTTP handler panic")
				writeError(w, model.Error(500, "internal_error", "内部错误"))
			}
		}()
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.validOrigin(r) {
				writeError(w, model.Error(403, "invalid_origin", "请求来源不允许"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) validOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.Path != "" || u.User != nil {
		return false
	}
	if s.publicURL != nil {
		return u.Scheme == s.publicURL.Scheme && strings.EqualFold(u.Host, s.publicURL.Host)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}
func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.Auth.Session(r)
		if !ok {
			writeError(w, model.Error(401, "unauthenticated", "请先登录"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !auth.VerifyCSRF(session, r.Header.Get("X-CSRF-Token")) {
			writeError(w, model.Error(403, "invalid_csrf", "CSRF 校验失败，请刷新页面"))
			return
		}
		next(w, r)
	}
}
func source(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	return host
}
func (s *Server) limit(w http.ResponseWriter, r *http.Request, scope string, max int) bool {
	if !s.Auth.Limit(scope+":"+source(r), max) {
		w.Header().Set("Retry-After", "60")
		writeError(w, model.Error(429, "rate_limited", "请求过于频繁，请稍后重试"))
		return false
	}
	return true
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	session, ok := s.Auth.Session(r)
	reply(w, 200, map[string]any{"initialized": s.Auth.Initialized(), "authenticated": ok, "csrf_token": session.CSRF})
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Initialized() {
		writeError(w, model.Error(409, "already_initialized", "管理员已设置"))
		return
	}
	if !s.limit(w, r, "setup", 5) {
		return
	}
	var v struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &v) {
		return
	}
	if e := s.Auth.Setup(v.Token, v.Password); e != nil {
		writeError(w, e)
		return
	}
	reply(w, 201, map[string]bool{"initialized": true})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.limit(w, r, "login", 10) {
		return
	}
	var v struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &v) {
		return
	}
	token, session, e := s.Auth.Login(v.Password)
	if e != nil {
		writeError(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil || s.publicURL != nil && s.publicURL.Scheme == "https", SameSite: http.SameSiteStrictMode, MaxAge: 43200, Expires: session.Expires})
	reply(w, 200, map[string]string{"csrf_token": session.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(r)
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}

type deviceInput struct {
	model.Device
	Password      *string `json:"vnc_password,omitempty"`
	ClearPassword bool    `json:"clear_vnc_password,omitempty"`
}

func (s *Server) saveDevice(w http.ResponseWriter, r *http.Request) {
	var v deviceInput
	if !decode(w, r, &v) {
		return
	}
	create := r.Method == "POST"
	if !create && v.ID != r.PathValue("id") {
		writeError(w, model.Error(422, "id_mismatch", "设备 ID 不一致"))
		return
	}
	d, e := s.App.SaveDevice(v.Device, v.Password, v.ClearPassword, create)
	if e != nil {
		writeError(w, e)
		return
	}
	status := 200
	if create {
		status = 201
	}
	reply(w, status, d)
}
func revision(w http.ResponseWriter, r *http.Request) (int, bool) {
	v, e := strconv.Atoi(strings.Trim(r.Header.Get("If-Match"), "\""))
	if e != nil || v < 1 {
		writeError(w, model.Error(422, "revision_required", "请通过 If-Match 提供设备 revision"))
		return 0, false
	}
	return v, true
}
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	v, ok := revision(w, r)
	if !ok {
		return
	}
	if e := s.App.DeleteDevice(r.PathValue("id"), v); e != nil {
		writeError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) credential(w http.ResponseWriter, r *http.Request) {
	v, ok := revision(w, r)
	if !ok {
		return
	}
	var password *string
	if r.Method == "PUT" {
		var input struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		password = &input.Password
	}
	if e := s.App.SetCredential(r.PathValue("id"), password, v); e != nil {
		writeError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Limit("operations:global", 60) {
		w.Header().Set("Retry-After", "60")
		writeError(w, model.Error(429, "rate_limited", "操作过于频繁"))
		return
	}
	action := "wake"
	if strings.HasSuffix(r.URL.Path, "/probe") {
		action = "probe"
	}
	now := time.Now().UTC()
	key := r.Header.Get("Idempotency-Key")
	if !model.ValidID(key) {
		writeError(w, model.Error(422, "idempotency_key_required", "请提供 1–64 位字母、数字、下划线或连字符 Idempotency-Key"))
		return
	}
	j, e := s.App.Submit(r.Context(), model.Command{SchemaVersion: model.Schema, CommandID: key, Action: action, DeviceID: r.PathValue("id"), IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if e != nil {
		writeError(w, e)
		return
	}
	w.Header().Set("Location", "/api/v1/wake-jobs/"+j.ID)
	reply(w, 202, j)
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, e := s.App.Get(r.PathValue("id"))
	if e != nil {
		writeError(w, e)
		return
	}
	reply(w, 200, j)
}
func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 100 {
			writeError(w, model.Error(422, "invalid_limit", "limit 须为 1–100"))
			return
		}
		limit = v
	}
	after := r.URL.Query().Get("after")
	started := after == ""
	out := []model.Job{}
	next := ""
	for _, j := range s.App.Jobs() {
		if !started {
			if j.ID == after {
				started = true
			}
			continue
		}
		if id := r.URL.Query().Get("device_id"); id != "" && j.DeviceID != id {
			continue
		}
		if status := r.URL.Query().Get("status"); status != "" && j.Status != status {
			continue
		}
		if len(out) == limit {
			next = out[len(out)-1].ID
			break
		}
		out = append(out, j)
	}
	reply(w, 200, map[string]any{"jobs": out, "next_cursor": next})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var v model.Config
	if !decode(w, r, &v) {
		return
	}
	if s.publicURL != nil && v.PublicURL != s.publicURL.String() || s.publicURL == nil && v.PublicURL != "" {
		u, e := url.Parse(v.PublicURL)
		if v.PublicURL != "" && (e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http")) {
			writeError(w, model.Error(422, "invalid_settings", "public_url 无效"))
			return
		}
	}
	if e := s.App.UpdateSettings(v); e != nil {
		writeError(w, e)
		return
	}
	runtime := s.App.Config()
	restartRequired := runtime.Listen != v.Listen || runtime.PublicURL != v.PublicURL || runtime.MaxConcurrent != v.MaxConcurrent || runtime.MaxVNC != v.MaxVNC || runtime.QueueSize != v.QueueSize || runtime.RetentionDays != v.RetentionDays
	reply(w, 200, map[string]bool{"saved": true, "restart_required": restartRequired})
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	d := s.App.Devices()
	for i := range d.Devices {
		d.Devices[i].PasswordConfigured = false
		d.Devices[i].ActiveJobID = ""
	}
	w.Header().Set("Content-Disposition", `attachment; filename="remotewake-devices.json"`)
	reply(w, 200, d)
}
func (s *Server) importDevices(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Devices  []model.Device `json:"devices"`
		Commit   bool           `json:"commit"`
		Revision int            `json:"revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if e := s.App.Import(input.Devices, input.Revision, input.Commit); e != nil {
		writeError(w, e)
		return
	}
	reply(w, 200, map[string]any{"valid": true, "committed": input.Commit, "count": len(input.Devices)})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		writeError(w, model.Error(415, "json_required", "需要 application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, e := io.ReadAll(r.Body)
	if e != nil {
		writeError(w, model.Error(413, "request_too_large", "请求过大"))
		return false
	}
	if e = storage.Decode(b, v); e != nil {
		writeError(w, model.Error(400, "invalid_json", "JSON 格式、字段或类型无效"))
		return false
	}
	return true
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, e error) {
	var f *model.Fault
	if !errors.As(e, &f) {
		f = &model.Fault{Status: 500, Code: "internal_error", Message: "内部错误"}
		slog.Error("请求处理失败", "error_type", fmt.Sprintf("%T", e))
	}
	v := map[string]any{"code": f.Code, "message": f.Message}
	if f.JobID != "" {
		v["job_id"] = f.JobID
	}
	reply(w, f.Status, map[string]any{"error": v})
}
