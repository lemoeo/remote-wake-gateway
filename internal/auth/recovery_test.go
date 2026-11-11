package auth

import (
	"bytes"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
	"net/http"
	"os"
	"testing"
)

func TestLocalPasswordRecoveryPreservesDataAndInvalidatesOldLogin(t *testing.T) {
	fs, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	a, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Setup(a.Token(), "old-admin-password"); e != nil {
		t.Fatal(e)
	}
	oldToken, _, e := a.Login("old-admin-password")
	if e != nil {
		t.Fatal(e)
	}
	names := []string{"devices.json", "secrets/credentials.json", "secrets/credentials.key", "jobs/job_example.json"}
	before := map[string][]byte{}
	for _, name := range names {
		b := []byte("preserve exactly: " + name)
		if e = fs.WriteBytes(name, b); e != nil {
			t.Fatal(e)
		}
		before[name] = b
	}
	oldAdmin, _ := os.ReadFile(fs.Path("secrets/admin.json"))
	if e = ResetPassword(fs, "short"); e == nil {
		t.Fatal("short password accepted")
	}
	unchanged, _ := os.ReadFile(fs.Path("secrets/admin.json"))
	if !bytes.Equal(oldAdmin, unchanged) {
		t.Fatal("failed reset changed admin data")
	}
	if e = ResetPassword(fs, "new-admin-password"); e != nil {
		t.Fatal(e)
	}
	reopened, e := New(fs)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = reopened.Login("old-admin-password"); e == nil {
		t.Fatal("old password remains valid")
	}
	if _, _, e = reopened.Login("new-admin-password"); e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("GET", "http://localhost/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: oldToken})
	if _, ok := reopened.Session(req); ok {
		t.Fatal("old in-memory session survived restart")
	}
	for _, name := range names {
		b, e := os.ReadFile(fs.Path(name))
		if e != nil || !bytes.Equal(b, before[name]) {
			t.Fatalf("recovery changed %s", name)
		}
	}
}
func TestRecoveryRequiresAnInitializedAdministrator(t *testing.T) {
	fs, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer fs.Close()
	if e = ResetPassword(fs, "new-admin-password"); e == nil {
		t.Fatal("recovery initialized a missing admin")
	}
	if _, e = os.Stat(fs.Path("secrets/admin.json")); !os.IsNotExist(e) {
		t.Fatal("unexpected admin file")
	}
}
