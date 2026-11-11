//go:build linux || darwin || freebsd

package wol

import (
	"net"
	"syscall"
)

func enableBroadcast(c *net.UDPConn) error {
	rc, e := c.SyscallConn()
	if e != nil {
		return e
	}
	var optionErr error
	e = rc.Control(func(fd uintptr) {
		optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
	if e != nil {
		return e
	}
	return optionErr
}
