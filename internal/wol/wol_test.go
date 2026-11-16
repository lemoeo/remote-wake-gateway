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
	conn, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	// Use loopback unicast so this test never sends a wake packet to a real LAN.
	w := model.WoL{MAC: "02:11:22:33:44:55", Broadcast: "127.0.0.1", Port: conn.LocalAddr().(*net.UDPAddr).Port, Repeat: 1, IntervalMS: 100}
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
}

func TestInterfacesExcludeLoopback(t *testing.T) {
	list, err := Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range list {
		iface, err := net.InterfaceByName(address.Name)
		if err != nil {
			t.Fatal(err)
		}
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			t.Fatalf("unavailable or loopback interface listed: %+v", address)
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			if _, err := Source(model.WoL{Interface: iface.Name, Broadcast: "127.0.0.1"}); err == nil {
				t.Fatalf("loopback interface accepted as manual source: %s", iface.Name)
			}
		}
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
