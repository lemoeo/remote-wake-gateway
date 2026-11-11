package wol

import (
	"context"
	"fmt"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"net"
	"time"
)

type Interface struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Broadcast string `json:"broadcast"`
	CIDR      string `json:"cidr"`
}

func Interfaces() ([]Interface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := []Interface{}
	for _, i := range list {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, e := i.Addrs()
		if e != nil {
			continue
		}
		for _, a := range addrs {
			ip, n, e := net.ParseCIDR(a.String())
			if e != nil || ip.To4() == nil {
				continue
			}
			v := ip.To4()
			b := make(net.IP, 4)
			for k := range b {
				b[k] = v[k] | ^n.Mask[k]
			}
			out = append(out, Interface{Name: i.Name, IP: v.String(), Broadcast: b.String(), CIDR: a.String()})
		}
	}
	return out, nil
}
func Source(w model.WoL) (net.IP, error) {
	// A nil source delegates address selection to the OS routing table.
	if w.Interface == "" {
		return nil, nil
	}
	iface, e := net.InterfaceByName(w.Interface)
	if e != nil {
		return nil, fmt.Errorf("发送网卡 %s 不存在或无法读取", w.Interface)
	}
	if iface.Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("发送网卡 %s 未启用", w.Interface)
	}
	list, e := Interfaces()
	if e != nil {
		return nil, e
	}
	return sourceFor(w.Interface, w.Broadcast, list)
}

// Prefer an address on the target subnet on interfaces with multiple IPv4
// addresses. A custom destination is allowed; fall back only within this NIC.
func sourceFor(name, broadcast string, list []Interface) (net.IP, error) {
	var first, subnet net.IP
	target := net.ParseIP(broadcast)
	for _, i := range list {
		ip := net.ParseIP(i.IP).To4()
		if i.Name != name || ip == nil {
			continue
		}
		if first == nil {
			first = ip
		}
		if broadcast == i.Broadcast {
			return ip, nil
		}
		if _, network, e := net.ParseCIDR(i.CIDR); e == nil && network.Contains(target) && subnet == nil {
			subnet = ip
		}
	}
	if subnet != nil {
		return subnet, nil
	}
	if first == nil {
		return nil, fmt.Errorf("发送网卡 %s 没有可用的 IPv4 地址", name)
	}
	return first, nil
}
func Packet(mac string) ([]byte, error) {
	m, e := net.ParseMAC(mac)
	if e != nil || len(m) != 6 {
		return nil, fmt.Errorf("无效 MAC")
	}
	b := make([]byte, 102)
	for i := 0; i < 6; i++ {
		b[i] = 255
	}
	for i := 0; i < 16; i++ {
		copy(b[6+i*6:], m)
	}
	return b, nil
}
func Send(ctx context.Context, w model.WoL) error {
	source, e := Source(w)
	if e != nil {
		return e
	}
	return sendPackets(ctx, w, source)
}
func sendPackets(ctx context.Context, w model.WoL, source net.IP) error {
	b, e := Packet(w.MAC)
	if e != nil {
		return e
	}
	var local *net.UDPAddr
	if source != nil {
		local = &net.UDPAddr{IP: source}
	}
	target := &net.UDPAddr{IP: net.ParseIP(w.Broadcast), Port: w.Port}
	conn, e := net.DialUDP("udp4", local, target)
	if e != nil {
		return e
	}
	defer conn.Close()
	// net.DialUDP enables SO_BROADCAST for IPv4 UDP sockets on our supported
	// platforms, including Windows. No extra platform-specific setup is needed.
	for i := 0; i < w.Repeat; i++ {
		if e = ctx.Err(); e != nil {
			return e
		}
		conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, e = conn.Write(b); e != nil {
			return e
		}
		if i+1 < w.Repeat {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(w.IntervalMS) * time.Millisecond):
			}
		}
	}
	return nil
}
