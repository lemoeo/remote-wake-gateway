package wol

import (
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsStandardUDPEnablesBroadcast(t *testing.T) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		value, socketErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST)
	})
	if err != nil || socketErr != nil || value != 1 {
		t.Fatalf("SO_BROADCAST not enabled: value=%d control=%v socket=%v", value, err, socketErr)
	}
}
