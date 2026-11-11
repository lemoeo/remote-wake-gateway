package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"os"
)

type Record struct {
	Binding    string `json:"binding"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}
type file struct {
	SchemaVersion int               `json:"schema_version"`
	Records       map[string]Record `json:"records"`
}

// Caller serializes all access through the application mutex.
type Store struct {
	fs   *storage.Store
	data file
	aead cipher.AEAD
}

func Binding(d model.Device) string {
	b, _ := json.Marshal([]any{model.Schema, d.ID, d.IP, d.VNC.Port, d.VNC.Username})
	return string(b)
}
func Open(fs *storage.Store) (*Store, error) {
	data := file{SchemaVersion: model.Schema, Records: map[string]Record{}}
	if err := fs.Read("secrets/credentials.json", &data); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if data.SchemaVersion != model.Schema || data.Records == nil {
		return nil, errors.New("凭据格式不受支持")
	}
	key, err := os.ReadFile(fs.Path("secrets/credentials.key"))
	if errors.Is(err, os.ErrNotExist) && len(data.Records) == 0 {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err == nil {
			err = fs.WriteBytes("secrets/credentials.key", key)
		}
	}
	if err != nil {
		return nil, errors.New("凭据密钥缺失或无法读取，请恢复密钥")
	}
	defer clear(key)
	if len(key) != 32 {
		return nil, errors.New("凭据密钥长度无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{fs: fs, data: data, aead: aead}, nil
}
func (s *Store) Has(d model.Device) bool {
	r, ok := s.data.Records[d.ID]
	return ok && r.Binding == Binding(d)
}
func (s *Store) Get(d model.Device) ([]byte, error) {
	r, ok := s.data.Records[d.ID]
	if !ok || r.Binding != Binding(d) {
		return nil, errors.New("请为当前目标配置 VNC 密码")
	}
	n, e := base64.StdEncoding.DecodeString(r.Nonce)
	if e != nil || len(n) != s.aead.NonceSize() {
		return nil, errors.New("凭据损坏")
	}
	c, e := base64.StdEncoding.DecodeString(r.Ciphertext)
	if e != nil {
		return nil, errors.New("凭据损坏")
	}
	p, e := s.aead.Open(nil, n, c, []byte(r.Binding))
	if e != nil {
		return nil, errors.New("无法解密凭据，请检查密钥")
	}
	return p, nil
}
func (s *Store) Set(d model.Device, password string) error {
	if !model.CredentialText(password) {
		return errors.New("密码须为 1–63 UTF-8 字节，不能包含 NUL 或换行")
	}
	n := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return e
	}
	b := Binding(d)
	p := []byte(password)
	defer clear(p)
	record := Record{Binding: b, Nonce: base64.StdEncoding.EncodeToString(n), Ciphertext: base64.StdEncoding.EncodeToString(s.aead.Seal(nil, n, p, []byte(b)))}
	next := s.copy()
	next.Records[d.ID] = record
	if e := s.fs.Write("secrets/credentials.json", next); e != nil {
		return e
	}
	s.data = next
	return nil
}
func (s *Store) Delete(id string) error {
	next := s.copy()
	delete(next.Records, id)
	if e := s.fs.Write("secrets/credentials.json", next); e != nil {
		return e
	}
	s.data = next
	return nil
}
func (s *Store) Prune(live map[string]bool) error {
	next := s.copy()
	changed := false
	for id := range next.Records {
		if !live[id] {
			delete(next.Records, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if e := s.fs.Write("secrets/credentials.json", next); e != nil {
		return e
	}
	s.data = next
	return nil
}
func (s *Store) copy() file {
	next := file{SchemaVersion: model.Schema, Records: map[string]Record{}}
	for k, v := range s.data.Records {
		next.Records[k] = v
	}
	return next
}
