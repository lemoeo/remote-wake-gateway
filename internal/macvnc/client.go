// Package macvnc implements only the authentication stage of Apple Screen Sharing.
// It never requests a desktop session, framebuffer, input or clipboard data.
package macvnc

import (
	"context"
	"crypto/aes"
	"crypto/md5" // Apple DH wire format requires MD5; never used for stored passwords.
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"io"
	"math/big"
	"net"
	"strconv"
	"time"
)

type Failure struct{ Code string }

func (f *Failure) Error() string { return f.Code }
func fail(code string) error     { return &Failure{Code: code} }

type Evidence struct {
	Connected     bool
	AuthSent      bool
	Authenticated bool
}
type Options struct {
	IP           string
	Port         int
	Username     string
	Password     []byte
	Hold         time.Duration
	AuthDeadline time.Time
	BeforeAuth   func() error
	Progress     func(string)
}

func Run(ctx context.Context, o Options) (ev Evidence, err error) {
	if !model.CredentialText(o.Username) || !model.CredentialText(string(o.Password)) {
		return ev, fail("invalid_credentials")
	}
	report := func(p string) {
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	authCtx, cancel := context.WithDeadline(ctx, o.AuthDeadline)
	defer cancel()
	var c net.Conn
	report("connecting")
	for {
		c, err = (&net.Dialer{Timeout: 3 * time.Second}).DialContext(authCtx, "tcp", net.JoinHostPort(o.IP, strconv.Itoa(o.Port)))
		if err == nil {
			break
		}
		select {
		case <-authCtx.Done():
			return ev, authCtx.Err()
		case <-time.After(time.Second):
		}
	}
	ev.Connected = true
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if err = c.SetDeadline(o.AuthDeadline); err != nil {
		return ev, err
	}
	if err = negotiate(c); err != nil {
		return ev, err
	}
	report("authenticating")
	// Build and validate all DH fields before marking an authentication attempt.
	response, err := response(c, o.Username, o.Password)
	if err != nil {
		return ev, err
	}
	defer clear(response)
	if err = authCtx.Err(); err != nil {
		return ev, err
	}
	if o.BeforeAuth != nil {
		if err = o.BeforeAuth(); err != nil {
			return ev, err
		}
	}
	ev.AuthSent = true
	if err = write(c, response); err != nil {
		return ev, fail("auth_result_unknown")
	}
	var result [4]byte
	if _, err = io.ReadFull(c, result[:]); err != nil {
		return ev, fail("auth_result_unknown")
	}
	switch binary.BigEndian.Uint32(result[:]) {
	case 0:
		ev.Authenticated = true
	case 1:
		return ev, fail("auth_rejected")
	case 2:
		return ev, fail("auth_limited")
	default:
		return ev, fail("auth_result_unknown")
	}
	report("authenticated")
	if o.Hold > 0 {
		report("holding")
		if err = hold(ctx, c, o.Hold); err != nil {
			return ev, err
		}
	}
	if err = ctx.Err(); err != nil {
		return ev, err
	}
	return ev, nil
}
func negotiate(c net.Conn) error {
	var banner [12]byte
	if _, e := io.ReadFull(c, banner[:]); e != nil {
		return fail("rfb_read_failed")
	}
	if string(banner[:4]) != "RFB " || banner[7] != '.' || banner[11] != '\n' {
		return fail("rfb_version_invalid")
	}
	for _, i := range []int{4, 5, 6, 8, 9, 10} {
		if banner[i] < '0' || banner[i] > '9' {
			return fail("rfb_version_invalid")
		}
	}
	major, _ := strconv.Atoi(string(banner[4:7]))
	minor, _ := strconv.Atoi(string(banner[8:11]))
	if major != 3 || minor < 7 {
		return fail("rfb_version_unsupported")
	}
	version := "RFB 003.008\n"
	if minor == 7 {
		version = "RFB 003.007\n"
	}
	if e := write(c, []byte(version)); e != nil {
		return fail("rfb_write_failed")
	}
	var n [1]byte
	if _, e := io.ReadFull(c, n[:]); e != nil || n[0] == 0 {
		return fail("rfb_security_unavailable")
	}
	types := make([]byte, int(n[0]))
	if _, e := io.ReadFull(c, types); e != nil {
		return fail("rfb_read_failed")
	}
	for _, t := range types {
		if t == 30 {
			return write(c, []byte{30})
		}
	}
	return fail("apple_dh_unsupported")
}
func response(r io.Reader, user string, password []byte) ([]byte, error) {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, fail("dh_read_failed")
	}
	g := int64(binary.BigEndian.Uint16(h[:2]))
	size := int(binary.BigEndian.Uint16(h[2:]))
	if size < 128 || size > 512 {
		return nil, fail("dh_parameters_invalid")
	}
	fields := make([]byte, size*2)
	if _, e := io.ReadFull(r, fields); e != nil {
		return nil, fail("dh_read_failed")
	}
	p := new(big.Int).SetBytes(fields[:size])
	peer := new(big.Int).SetBytes(fields[size:])
	upper := new(big.Int).Sub(p, big.NewInt(2))
	if p.BitLen() < 1024 || p.Bit(0) == 0 || g < 2 || big.NewInt(g).Cmp(upper) > 0 || peer.Cmp(big.NewInt(2)) < 0 || peer.Cmp(upper) > 0 || !p.ProbablyPrime(20) {
		return nil, fail("dh_parameters_invalid")
	}
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		return nil, e
	}
	defer clear(secret)
	x := new(big.Int).SetBytes(secret)
	defer x.SetInt64(0)
	if x.Sign() == 0 {
		return nil, fail("dh_random_failed")
	}
	shared := new(big.Int).Exp(peer, x, p)
	defer shared.SetInt64(0)
	if shared.Cmp(big.NewInt(2)) < 0 {
		return nil, fail("dh_parameters_invalid")
	}
	raw := make([]byte, size)
	defer clear(raw)
	shared.FillBytes(raw)
	key := md5.Sum(raw)
	defer clear(key[:])
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	out := make([]byte, 128+size)
	if _, e = rand.Read(out[:128]); e != nil {
		return nil, e
	}
	copy(out[:64], user)
	out[len(user)] = 0
	copy(out[64:128], password)
	out[64+len(password)] = 0
	for i := 0; i < 128; i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], out[i:i+aes.BlockSize])
	}
	new(big.Int).Exp(big.NewInt(g), x, p).FillBytes(out[128:])
	return out, nil
}
func hold(ctx context.Context, c net.Conn, d time.Duration) error {
	end := time.Now().Add(d)
	deadline := end
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	if e := c.SetReadDeadline(deadline); e != nil {
		return e
	}
	var buf [1024]byte
	total := 0
	for {
		n, e := c.Read(buf[:])
		total += n
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if total > 65536 {
			return fail("hold_data_limit")
		}
		if e != nil {
			var ne net.Error
			if errors.As(e, &ne) && ne.Timeout() && !time.Now().Before(end) {
				return nil
			}
			return fail("hold_disconnected")
		}
		if !time.Now().Before(end) {
			return nil
		}
	}
}
func write(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func Code(e error) string {
	var f *Failure
	if errors.As(e, &f) {
		return f.Code
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(e, context.Canceled) {
		return "cancelled"
	}
	return "connection_failed"
}
func Message(code string) string {
	switch code {
	case "auth_rejected":
		return "Mac 拒绝了凭据，请检查账户和屏幕共享权限"
	case "auth_limited":
		return "Mac 已限制认证，请人工检查账户后解除保护"
	case "auth_result_unknown":
		return "认证结果不明，已停止重试"
	case "apple_dh_unsupported":
		return "服务端不支持 Apple DH(30) 认证"
	case "hold_disconnected":
		return "保持期间服务端断开连接"
	case "timeout":
		return "等待或执行超时"
	case "cancelled":
		return "任务已取消"
	default:
		return fmt.Sprintf("VNC 步骤未完成（%s）", code)
	}
}
