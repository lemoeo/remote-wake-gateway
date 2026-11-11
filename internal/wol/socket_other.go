//go:build !linux && !darwin && !freebsd

package wol

import (
	"errors"
	"net"
)

func enableBroadcast(c *net.UDPConn) error { return errors.New("该平台暂不支持广播") }
