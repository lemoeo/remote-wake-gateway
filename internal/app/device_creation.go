package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

// Deriving the internal identity from an opaque request key makes successful
// creation retries recognizable even after restart, without storing passwords
// or request fingerprints in public configuration.
func (s *Service) CreateDevice(d model.Device, password *string, clearPassword bool, key string) (model.Device, error) {
	if key == "" {
		return s.SaveDevice(d, password, clearPassword, true)
	}
	if !model.ValidID(key) {
		return d, model.Error(422, "invalid_idempotency_key", "创建设备请求标识无效")
	}
	hash := sha256.Sum256([]byte("device-create:" + key))
	id := "dev_" + hex.EncodeToString(hash[:16])
	if d.ID != "" && d.ID != id {
		return d, model.Error(422, "idempotency_id_conflict", "使用创建请求标识时不能指定其他设备 ID")
	}
	d.ID = id
	return s.saveDevice(d, password, clearPassword, true, true)
}

func (s *Service) replayDeviceCreationLocked(input, saved model.Device, password *string, clearPassword bool) (model.Device, error) {
	want, got := input, saved
	want.Revision = 0
	got.Revision = 0
	want.ActiveJobID = ""
	got.ActiveJobID = ""
	want.PasswordConfigured = false
	got.PasswordConfigured = false
	conflict := func() (model.Device, error) {
		return input, model.Error(409, "idempotency_conflict", "此创建请求已保存，但设备配置已变化；请关闭表单并查看设备列表")
	}
	if want != got || password != nil && clearPassword {
		return conflict()
	}
	configured := s.creds.Has(saved)
	if password != nil {
		stored, err := s.creds.Get(saved)
		if err != nil {
			return conflict()
		}
		defer clear(stored)
		if subtle.ConstantTimeCompare(stored, []byte(*password)) != 1 {
			return conflict()
		}
	} else if configured {
		return conflict()
	}
	saved.PasswordConfigured = configured
	saved.ActiveJobID = s.active[saved.ID]
	return saved, nil
}
