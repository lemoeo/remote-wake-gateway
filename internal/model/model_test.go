package model

import (
	"encoding/json"
	"testing"
)

func TestWoLDestinationValidation(t *testing.T) {
	cfg := DefaultConfig()
	for _, tc := range []struct {
		target string
		valid  bool
	}{
		{"255.255.255.255", true},
		{"192.168.99.255", true},
		{"10.0.0.255", true},
		{"203.0.113.255", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"192.168.1.999", false},
		{"", false},
	} {
		t.Run(tc.target, func(t *testing.T) {
			d := Device{ID: "pc", Name: "PC", WakeMethod: "wol", WoL: WoL{MAC: "02:11:22:33:44:55", Broadcast: tc.target}}
			d.Defaults()
			if e := d.Validate(cfg); (e == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, e)
			}
		})
	}
	// Even a broad allowlist must not turn unspecified or multicast addresses
	// into valid WoL destinations. Limited broadcast is the only policy exception.
	cfg.AllowedNetworks = []string{"0.0.0.0/0"}
	for _, target := range []string{"0.0.0.0", "224.0.0.1"} {
		d := Device{ID: "pc", Name: "PC", WakeMethod: "wol", WoL: WoL{MAC: "02:11:22:33:44:55", Broadcast: target}}
		d.Defaults()
		if e := d.Validate(cfg); e == nil {
			t.Fatalf("accepted invalid destination %s", target)
		}
	}
}

func TestLegacyConfigurationAndDefaultParameterValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Listen != "127.0.0.1:9000" {
		t.Fatalf("unexpected default listener: %s", cfg.Listen)
	}
	cfg.Listen = "127.0.0.1:8080" // Existing explicit ports remain valid.
	raw, e := json.Marshal(cfg)
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	delete(fields, "device_defaults")
	raw, _ = json.Marshal(fields)
	var legacy Config
	if e = json.Unmarshal(raw, &legacy); e != nil {
		t.Fatal(e)
	}
	legacy.Defaults()
	if legacy.Listen != cfg.Listen {
		t.Fatal("normalizing defaults changed an existing listener")
	}
	if legacy.DeviceDefaults != DefaultDeviceDefaults() {
		t.Fatal("legacy defaults not normalized")
	}
	for _, defaults := range []DeviceDefaults{
		{Broadcast: "255.255.255.255", Port: 0, Repeat: 3},
		{Broadcast: "255.255.255.255", Port: 9, Repeat: 11},
		{Broadcast: "203.0.113.255", Port: 9, Repeat: 3},
		{Broadcast: "0.0.0.0", Port: 9, Repeat: 3},
	} {
		cfg.DeviceDefaults = defaults
		if e = cfg.Validate(); e == nil {
			t.Fatalf("invalid defaults accepted: %+v", defaults)
		}
	}
}
