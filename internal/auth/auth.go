package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"os"
	"sync"
	"time"
)

const CookieName = "remotewake_session"

type admin struct {
	SchemaVersion int    `json:"schema_version"`
	PasswordHash  string `json:"password_hash"`
}
type Session struct {
	CSRF    string
	Expires time.Time
}
type bucket struct {
	Count int
	Until time.Time
}
type Auth struct {
	mu        sync.Mutex
	fs        *storage.Store
	admin     admin
	token     string
	sessions  map[[32]byte]Session
	limits    map[string]bucket
	hashSlots chan struct{}
}

func New(fs *storage.Store) (*Auth, error) {
	a := &Auth{fs: fs, admin: admin{SchemaVersion: model.Schema}, sessions: map[[32]byte]Session{}, limits: map[string]bucket{}, hashSlots: make(chan struct{}, 2)}
	if e := fs.Read("secrets/admin.json", &a.admin); e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if a.admin.SchemaVersion != model.Schema {
		return nil, errors.New("管理员文件版本不支持")
	}
	if a.admin.PasswordHash != "" {
		if _, e := bcrypt.Cost([]byte(a.admin.PasswordHash)); e != nil {
			return nil, errors.New("管理员密码哈希损坏")
		}
		_ = os.Remove(fs.Path("secrets/setup.token"))
	} else {
		a.token = model.ID("setup_")
		if e := fs.WriteBytes("secrets/setup.token", []byte(a.token+"\n")); e != nil {
			return nil, e
		}
	}
	return a, nil
}
func (a *Auth) Initialized() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admin.PasswordHash != ""
}
func (a *Auth) Token() string { a.mu.Lock(); defer a.mu.Unlock(); return a.token }
func (a *Auth) Limit(key string, max int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	b, ok := a.limits[key]
	if !ok || now.After(b.Until) {
		for k, v := range a.limits {
			if now.After(v.Until) {
				delete(a.limits, k)
			}
		}
		if len(a.limits) >= 1024 {
			return false
		}
		b = bucket{Until: now.Add(time.Minute)}
	}
	b.Count++
	a.limits[key] = b
	return b.Count <= max
}
func (a *Auth) Setup(token, password string) error {
	if a.Initialized() {
		return model.Error(409, "already_initialized", "管理员已设置")
	}
	expected := sha256.Sum256([]byte(a.Token()))
	provided := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(expected[:], provided[:]) != 1 {
		return model.Error(403, "invalid_setup_token", "初始化令牌无效")
	}
	if len(password) < 12 || len(password) > 72 {
		return model.Error(422, "invalid_password", "管理密码须为 12–72 字节")
	}
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	default:
		return model.Error(503, "auth_busy", "密码处理繁忙")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin.PasswordHash != "" {
		return model.Error(409, "already_initialized", "管理员已设置")
	}
	next := admin{SchemaVersion: model.Schema, PasswordHash: string(hash)}
	if e = a.fs.Write("secrets/admin.json", next); e != nil {
		return model.Error(503, "storage_unavailable", "无法保存管理员设置")
	}
	a.admin = next
	a.token = ""
	_ = os.Remove(a.fs.Path("secrets/setup.token"))
	return nil
}
func (a *Auth) Login(password string) (string, Session, error) {
	a.mu.Lock()
	hash := a.admin.PasswordHash
	a.mu.Unlock()
	if hash == "" {
		return "", Session{}, model.Error(409, "setup_required", "请先完成初始化")
	}
	if len(password) < 1 || len(password) > 72 {
		return "", Session{}, model.Error(401, "invalid_login", "密码错误")
	}
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	default:
		return "", Session{}, model.Error(503, "auth_busy", "密码处理繁忙")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", Session{}, model.Error(401, "invalid_login", "密码错误")
	}
	token := model.ID("session_")
	session := Session{CSRF: model.ID("csrf_"), Expires: time.Now().Add(12 * time.Hour)}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.sessions {
		if time.Now().After(v.Expires) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 256 {
		return "", Session{}, model.Error(503, "session_limit", "会话数量已达上限")
	}
	a.sessions[sha256.Sum256([]byte(token))] = session
	return token, session, nil
}
func (a *Auth) Session(r *http.Request) (Session, bool) {
	c, e := r.Cookie(CookieName)
	if e != nil || len(c.Value) > 100 {
		return Session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := sha256.Sum256([]byte(c.Value))
	s, ok := a.sessions[key]
	if !ok || time.Now().After(s.Expires) {
		delete(a.sessions, key)
		return Session{}, false
	}
	return s, true
}
func (a *Auth) Logout(r *http.Request) {
	c, e := r.Cookie(CookieName)
	if e != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sha256.Sum256([]byte(c.Value)))
}
func VerifyCSRF(s Session, value string) bool {
	return subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(value)) == 1
}

// ResetPassword is a local recovery operation. The CLI must acquire the same
// exclusive data-directory lock as the server before calling it. No HTTP route
// exposes this function, and device data, VNC credentials and jobs are untouched.
func ResetPassword(fs *storage.Store, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return model.Error(422, "invalid_password", "管理密码须为 12–72 字节")
	}
	var current admin
	if e := fs.Read("secrets/admin.json", &current); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return errors.New("尚未设置管理员，请使用 init 初始化")
		}
		return e
	}
	if current.SchemaVersion != model.Schema {
		return errors.New("管理员文件版本不支持")
	}
	if current.PasswordHash == "" {
		return errors.New("尚未设置管理员，请使用 init 初始化")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	if e = fs.Write("secrets/admin.json", admin{SchemaVersion: model.Schema, PasswordHash: string(hash)}); e != nil {
		return e
	}
	_ = os.Remove(fs.Path("secrets/setup.token"))
	return nil
}
