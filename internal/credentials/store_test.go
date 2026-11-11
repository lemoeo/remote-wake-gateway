package credentials

import (
	"bytes"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"os"
	"testing"
)

func TestCredentialBindingAndMissingKey(t *testing.T) {
	fs, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	s, e := Open(fs)
	if e != nil {
		t.Fatal(e)
	}
	d := model.Device{ID: "mac", IP: "192.168.1.2", VNC: model.VNC{Port: 5900, Username: "alice"}}
	if e = s.Set(d, "test-password-secret"); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(fs.Path("secrets/credentials.json"))
	if bytes.Contains(raw, []byte("test-password-secret")) {
		t.Fatal("plaintext leaked")
	}
	p, e := s.Get(d)
	if e != nil || string(p) != "test-password-secret" {
		t.Fatal("roundtrip failed")
	}
	clear(p)
	d.IP = "192.168.1.3"
	if _, e = s.Get(d); e == nil || s.Has(d) {
		t.Fatal("credential redirected to new target")
	}
	if e = os.Remove(fs.Path("secrets/credentials.key")); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(fs); e == nil {
		t.Fatal("missing key silently replaced")
	}
	if _, e = os.Stat(fs.Path("secrets/credentials.key")); !os.IsNotExist(e) {
		t.Fatal("replacement key created")
	}
}
