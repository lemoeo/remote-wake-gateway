//go:build !windows

package macvnc

import (
	"errors"
	"syscall"
)

const (
	connectionRefused  = syscall.ECONNREFUSED
	networkUnreachable = syscall.ENETUNREACH
	hostUnreachable    = syscall.EHOSTUNREACH
	connectionTimedOut = syscall.ETIMEDOUT
)

func connectionErrorCode(err error) string {
	switch {
	case errors.Is(err, connectionRefused):
		return "connection_refused"
	case errors.Is(err, networkUnreachable), errors.Is(err, hostUnreachable):
		return "network_unreachable"
	case errors.Is(err, connectionTimedOut):
		return "connection_timeout"
	default:
		return ""
	}
}
