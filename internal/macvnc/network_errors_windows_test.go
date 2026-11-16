package macvnc

import (
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsConnectExErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{windows.ERROR_CONNECTION_REFUSED, "connection_refused"},
		{windows.ERROR_NETWORK_UNREACHABLE, "network_unreachable"},
		{windows.ERROR_HOST_UNREACHABLE, "network_unreachable"},
		{windows.ERROR_SEM_TIMEOUT, "connection_timeout"},
		{windows.ERROR_TIMEOUT, "connection_timeout"},
	} {
		err := &ConnectError{Err: &net.OpError{Op: "dial", Net: "tcp", Err: tc.cause}}
		if Code(err) != tc.code {
			t.Errorf("ConnectEx error %v: got %s, want %s", tc.cause, Code(err), tc.code)
		}
	}
}
