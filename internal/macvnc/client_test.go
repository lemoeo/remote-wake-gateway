package macvnc

import (
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestAppleDHWireAuthentication(t *testing.T) {
	p, e := rand.Prime(rand.Reader, 1024)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name       string
		result     uint32
		guardError bool
		want       string
	}{{"success", 0, false, ""}, {"wrong_password", 1, false, "auth_rejected"}, {"limited", 2, false, "auth_limited"}, {"persist_failed", 0, true, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			done := make(chan error, 1)
			sent := 0
			go func() {
				c, e := listener.Accept()
				if e != nil {
					done <- e
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				write(c, []byte("RFB 003.008\n"))
				version := make([]byte, 12)
				if _, e = io.ReadFull(c, version); e != nil {
					done <- e
					return
				}
				if string(version) != "RFB 003.008\n" {
					done <- fmt.Errorf("version mismatch")
					return
				}
				write(c, []byte{2, 1, 30})
				var security [1]byte
				io.ReadFull(c, security[:])
				if security[0] != 30 {
					done <- fmt.Errorf("security downgraded")
					return
				}
				secret := big.NewInt(123456789)
				serverPublic := new(big.Int).Exp(big.NewInt(2), secret, p)
				header := make([]byte, 4+256)
				binary.BigEndian.PutUint16(header[:2], 2)
				binary.BigEndian.PutUint16(header[2:4], 128)
				p.FillBytes(header[4:132])
				serverPublic.FillBytes(header[132:])
				write(c, header)
				response := make([]byte, 256)
				_, e = io.ReadFull(c, response)
				if tc.guardError {
					if e == nil {
						done <- fmt.Errorf("credentials sent despite failed durable guard")
					} else {
						done <- nil
					}
					return
				}
				if e != nil {
					done <- e
					return
				}
				sent++
				clientPublic := new(big.Int).SetBytes(response[128:])
				shared := new(big.Int).Exp(clientPublic, secret, p)
				raw := make([]byte, 128)
				shared.FillBytes(raw)
				key := md5.Sum(raw)
				block, _ := aes.NewCipher(key[:])
				for i := 0; i < 128; i += 16 {
					block.Decrypt(response[i:i+16], response[i:i+16])
				}
				if string(response[:6]) != "alice\x00" || string(response[64:71]) != "secret\x00" {
					done <- fmt.Errorf("wire credentials mismatch")
					return
				}
				result := make([]byte, 4)
				binary.BigEndian.PutUint32(result, tc.result)
				write(c, result)
				var next [1]byte
				n, e := c.Read(next[:])
				if n > 0 || e == nil {
					done <- fmt.Errorf("unexpected ClientInit or retry data")
					return
				}
				done <- nil
			}()
			ev, e := Run(context.Background(), Options{IP: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "alice", Password: []byte("secret"), AuthDeadline: time.Now().Add(4 * time.Second), BeforeAuth: func() error {
				if tc.guardError {
					return errors.New("disk full")
				}
				return nil
			}})
			if tc.want != "" && Code(e) != tc.want {
				t.Fatalf("want %s, got %v", tc.want, e)
			}
			if tc.name == "success" && (e != nil || !ev.Authenticated) {
				t.Fatalf("auth failed: %v", e)
			}
			if tc.guardError && ev.AuthSent {
				t.Fatal("guard bypassed")
			}
			if se := <-done; se != nil {
				t.Fatal(se)
			}
			if !tc.guardError && sent != 1 {
				t.Fatal("authentication count mismatch")
			}
		})
	}
}
func TestRejectUnsupportedSecurityWithoutDowngrade(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		write(b, []byte("RFB 003.008\n"))
		v := make([]byte, 12)
		io.ReadFull(b, v)
		write(b, []byte{1, 1})
	}()
	if Code(negotiate(a)) != "apple_dh_unsupported" {
		t.Fatal("accepted unauthenticated VNC")
	}
}
func TestBoundDHParameters(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { write(b, []byte{0, 2, 255, 255}) }()
	if _, e := response(a, "alice", []byte("secret")); Code(e) != "dh_parameters_invalid" {
		t.Fatal("unbounded parameter accepted")
	}
}
