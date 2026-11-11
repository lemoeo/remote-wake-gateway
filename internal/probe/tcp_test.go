package probe

import (
	"context"
	"net"
	"testing"
)

func TestTCP(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if e = TCP(context.Background(), "127.0.0.1", port); e != nil {
		t.Fatal(e)
	}
	l.Close()
	if e = TCP(context.Background(), "127.0.0.1", port); e == nil {
		t.Fatal("closed port reported reachable")
	}
}
