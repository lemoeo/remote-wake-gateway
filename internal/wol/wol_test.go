package wol

import (
	"bytes"
	"context"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"net"
	"testing"
	"time"
)

func TestMagicPacketAndLoopbackDelivery(t *testing.T) {
	p, e := Packet("02:11:22:33:44:55")
	if e != nil {
		t.Fatal(e)
	}
	if len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{255}, 6)) {
		t.Fatal("invalid prefix")
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+i*6:12+i*6], []byte{2, 17, 34, 51, 68, 85}) {
			t.Fatal("invalid repeated MAC")
		}
	}
	list, e := Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	var iface Interface
	for _, i := range list {
		if i.IP == "127.0.0.1" {
			iface = i
		}
	}
	if iface.Name == "" {
		t.Skip("no loopback IPv4")
	}
	conn, e := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	w := model.WoL{MAC: "02:11:22:33:44:55", Broadcast: iface.Broadcast, Interface: iface.Name, Port: conn.LocalAddr().(*net.UDPAddr).Port, Repeat: 1, IntervalMS: 100}
	source, e := Source(w)
	if e != nil {
		t.Fatal(e)
	}
	if !source.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("wrong manual source: %v", source)
	}
	// Use loopback unicast so this test never sends a wake packet to a real LAN.
	// An explicitly chosen NIC must also allow a custom destination.
	w.Broadcast = "127.0.0.1"
	if e = Send(context.Background(), w); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 256)
	n, _, e := conn.ReadFromUDP(buf)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(buf[:n], p) {
		t.Fatal("received wrong packet")
	}
	w.Interface = ""
	if e = Send(context.Background(), w); e != nil {
		t.Fatal(e)
	}
	n, _, e = conn.ReadFromUDP(buf)
	if e != nil || !bytes.Equal(buf[:n], p) {
		t.Fatalf("OS-selected source failed: %v", e)
	}
}

func TestSystemSourceDoesNotRequireBroadcastMatch(t *testing.T) {
	for _, target := range []string{"192.168.99.255", "255.255.255.255"} {
		ip, e := Source(model.WoL{Broadcast: target})
		if e != nil || ip != nil {
			t.Fatalf("system selection should use nil local address: %s %v %v", target, ip, e)
		}
	}
}

func TestManualSourceOnMultiAddressNIC(t *testing.T) {
	list := []Interface{
		{Name: "eth0", IP: "192.168.1.10", Broadcast: "192.168.1.255", CIDR: "192.168.1.10/24"},
		{Name: "eth0", IP: "192.168.2.10", Broadcast: "192.168.2.255", CIDR: "192.168.2.10/24"},
		{Name: "br-lan", IP: "10.0.0.1", Broadcast: "10.0.0.255", CIDR: "10.0.0.1/24"},
	}
	for _, tc := range []struct{ target, want string }{
		{"192.168.2.255", "192.168.2.10"},
		{"192.168.2.100", "192.168.2.10"},
		{"255.255.255.255", "192.168.1.10"},
		{"10.0.0.255", "192.168.1.10"},
	} {
		ip, e := sourceFor("eth0", tc.target, list)
		if e != nil || !ip.Equal(net.ParseIP(tc.want)) {
			t.Fatalf("target %s: got %v %v", tc.target, ip, e)
		}
	}
	if _, e := sourceFor("eth0", "255.255.255.255", []Interface{{Name: "br-lan", IP: "10.0.0.1"}, {Name: "eth0", IP: "::1"}}); e == nil {
		t.Fatal("NIC without IPv4 fell back to a different interface")
	}
}
func TestRejectWrongInterface(t *testing.T) {
	if _, e := Source(model.WoL{Interface: "nonexistent-remotewake", Broadcast: "192.168.1.255"}); e == nil {
		t.Fatal("silently selected another interface")
	}
}
