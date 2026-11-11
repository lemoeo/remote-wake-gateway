package storage

import (
	"os"
	"runtime"
	"testing"
)

func TestLockAndFailedReplacementPreserveData(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("second writer acquired lock")
	}
	if e = s.Write("config.json", map[string]int{"value": 7}); e != nil {
		t.Fatal(e)
	}
	if e = s.Write("missing/config.json", map[string]int{"value": 9}); e == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	var out map[string]int
	if e = s.Read("config.json", &out); e != nil || out["value"] != 7 {
		t.Fatalf("old data lost: %v", e)
	}
	info, _ := os.Stat(s.Path("config.json"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("unsafe permissions")
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}
func TestStrictJSON(t *testing.T) {
	for _, b := range []string{`{"field":1}{}`, `{"unexpected":2}`, `[]`} {
		var v struct {
			Field int `json:"field"`
		}
		if e := Decode([]byte(b), &v); e == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}
