package macvnc

import (
	"errors"

	"golang.org/x/sys/windows"
)

const (
	connectionRefused  = windows.WSAECONNREFUSED
	networkUnreachable = windows.WSAENETUNREACH
	hostUnreachable    = windows.WSAEHOSTUNREACH
	connectionTimedOut = windows.WSAETIMEDOUT
)

func connectionErrorCode(err error) string {
	// ConnectEx may return Win32 errors as well as Winsock errors.
	switch {
	case errors.Is(err, connectionRefused), errors.Is(err, windows.ERROR_CONNECTION_REFUSED):
		return "connection_refused"
	case errors.Is(err, networkUnreachable), errors.Is(err, hostUnreachable), errors.Is(err, windows.ERROR_NETWORK_UNREACHABLE), errors.Is(err, windows.ERROR_HOST_UNREACHABLE):
		return "network_unreachable"
	case errors.Is(err, connectionTimedOut), errors.Is(err, windows.ERROR_SEM_TIMEOUT), errors.Is(err, windows.ERROR_TIMEOUT):
		return "connection_timeout"
	default:
		return ""
	}
}
