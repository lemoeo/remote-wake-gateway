package macvnc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func connectionOptions() Options {
	return Options{IP: "127.0.0.1", Port: 5900, Username: "alice", Password: []byte("secret"), AuthDeadline: time.Now().Add(30 * time.Second)}
}

func TestSingleVNCDoesNotRetryConnectionFailure(t *testing.T) {
	o := connectionOptions()
	var phases []string
	o.Progress = func(phase string) { phases = append(phases, phase) }
	o.BeforeAuth = func() error { t.Fatal("TCP failure reached authentication"); return nil }
	calls := 0
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: connectionRefused}}
	ev, err := run(context.Background(), o, func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		if calls > 1 {
			t.Fatal("single VNC retried a failed connection")
		}
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(o.AuthDeadline) || network != "tcp" || address != "127.0.0.1:5900" {
			t.Fatal("connection did not share the authentication deadline")
		}
		return nil, cause
	})
	if calls != 1 || ev != (Evidence{}) || Code(err) != "connection_refused" || !errors.Is(err, cause) {
		t.Fatalf("lost connection failure: calls=%d evidence=%+v err=%v", calls, ev, err)
	}
	if !reflect.DeepEqual(phases, []string{"connecting"}) {
		t.Fatalf("unexpected progress: %v", phases)
	}
}

func TestSingleVNCReportsRealTCPRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	o := connectionOptions()
	o.Port = port
	o.AuthDeadline = time.Now().Add(2 * time.Second)
	ev, err := Run(context.Background(), o)
	if ev != (Evidence{}) || Code(err) != "connection_refused" {
		t.Fatalf("real TCP refusal was retried or misclassified: evidence=%+v err=%v", ev, err)
	}
}

func TestCombinedVNCRetriesTCPButNotProtocolFailure(t *testing.T) {
	o := connectionOptions()
	o.RetryConnect = true
	var phases []string
	o.Progress = func(phase string) { phases = append(phases, phase) }
	o.BeforeAuth = func() error { t.Fatal("unsupported protocol reached authentication"); return nil }
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		server.SetDeadline(time.Now().Add(5 * time.Second))
		if err := write(server, []byte("RFB 003.008\n")); err != nil {
			done <- err
			return
		}
		var version [12]byte
		if _, err := io.ReadFull(server, version[:]); err != nil {
			done <- err
			return
		}
		done <- write(server, []byte{1, 1})
	}()
	calls := 0
	firstFailure := time.Now()
	ev, err := run(context.Background(), o, func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls++
		if calls == 1 {
			firstFailure = time.Now()
			return nil, connectionRefused
		}
		if calls != 2 || time.Since(firstFailure) < time.Second {
			t.Fatal("TCP retry count or interval is wrong")
		}
		deadline, _ := ctx.Deadline()
		if !deadline.Equal(o.AuthDeadline) {
			t.Fatal("retry extended the authentication budget")
		}
		return client, nil
	})
	if calls != 2 || !ev.Connected || ev.AuthSent || Code(err) != "apple_dh_unsupported" {
		t.Fatalf("protocol failure retried: calls=%d evidence=%+v err=%v", calls, ev, err)
	}
	if !reflect.DeepEqual(phases, []string{"connecting", "waiting_network", "connecting"}) {
		t.Fatalf("unexpected progress: %v", phases)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConnectionBudgetPreservesLastTCPError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := connectionOptions()
	o.RetryConnect = true
	calls := 0
	ev, err := run(ctx, o, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, networkUnreachable
	})
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || calls != 1 || ev != (Evidence{}) || Code(err) != "network_unreachable" || !errors.Is(err, networkUnreachable) {
		t.Fatalf("budget replaced the TCP error: calls=%d evidence=%+v err=%v", calls, ev, err)
	}
}

func TestCancelDuringConnectionRetryStopsWithoutAuthentication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := connectionOptions()
	o.RetryConnect = true
	o.Progress = func(phase string) {
		if phase == "waiting_network" {
			cancel()
		}
	}
	calls := 0
	ev, err := run(ctx, o, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, connectionRefused
	})
	if calls != 1 || ev != (Evidence{}) || !errors.Is(err, context.Canceled) || Code(err) != "cancelled" {
		t.Fatalf("retry ignored cancellation: calls=%d evidence=%+v err=%v", calls, ev, err)
	}
}

func TestSingleTCPTimeoutKeepsConnectionSpecificCode(t *testing.T) {
	o := connectionOptions()
	o.AuthDeadline = time.Now().Add(25 * time.Millisecond)
	ev, err := run(context.Background(), o, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	var connectErr *ConnectError
	if ev != (Evidence{}) || !errors.As(err, &connectErr) || !errors.Is(err, context.DeadlineExceeded) || Code(err) != "connection_timeout" {
		t.Fatalf("TCP timeout became a generic timeout: evidence=%+v err=%v", ev, err)
	}
}

func TestConnectionErrorCodesAndMessages(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
		text  string
	}{
		{connectionRefused, "connection_refused", "TCP 连接被拒绝"},
		{networkUnreachable, "network_unreachable", "网络不可达"},
		{hostUnreachable, "network_unreachable", "网络不可达"},
		{connectionTimedOut, "connection_timeout", "TCP 连接超时"},
		{context.DeadlineExceeded, "connection_timeout", "TCP 连接超时"},
		{os.ErrDeadlineExceeded, "connection_timeout", "TCP 连接超时"},
		{io.ErrUnexpectedEOF, "connection_failed", "TCP 连接失败"},
	} {
		t.Run(tc.code+"/"+tc.cause.Error(), func(t *testing.T) {
			err := &ConnectError{Err: &net.OpError{Op: "dial", Net: "tcp", Err: tc.cause}}
			if Code(err) != tc.code || !strings.Contains(err.Error(), tc.text) || !strings.Contains(err.Error(), tc.cause.Error()) {
				t.Fatalf("wrong connection diagnosis: %v (%s)", err, Code(err))
			}
		})
	}
}
