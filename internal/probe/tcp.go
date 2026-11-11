package probe

import (
	"context"
	"net"
	"strconv"
	"time"
)

func TCP(ctx context.Context, ip string, port int) error {
	c, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if e != nil {
		return e
	}
	return c.Close()
}
func Wait(ctx context.Context, ip string, port int) error {
	for {
		if e := TCP(ctx, ip, port); e == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
