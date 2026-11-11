package auth

import (
	"strings"
	"testing"

	"github.com/lemoeo/remote-wake-gateway/internal/storage"
)

func TestSetupPasswordLengthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		valid    bool
	}{
		{"empty", "", false},
		{"five_bytes", "abcde", false},
		{"six_bytes", "abcdef", true},
		{"maximum", strings.Repeat("a", 72), true},
		{"too_long", strings.Repeat("a", 73), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			a, err := New(fs)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.Setup(a.Token(), tc.password); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
			if a.Initialized() != tc.valid {
				t.Fatal("unexpected initialization state")
			}
			if tc.valid {
				if _, _, err = a.Login(tc.password); err != nil {
					t.Fatalf("cannot log in with accepted password: %v", err)
				}
			}
		})
	}
}
