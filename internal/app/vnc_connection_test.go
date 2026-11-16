package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

func TestVNCConnectionPolicyAndFailureDiagnosis(t *testing.T) {
	for _, method := range []string{"mac_vnc", "wol_mac_vnc"} {
		for _, timeout := range []bool{false, true} {
			name := method + "/connection_failed"
			cause := errors.New("dial tcp: network failure")
			wantCode := "connection_failed"
			if timeout {
				name = method + "/connection_timeout"
				cause = context.DeadlineExceeded
				wantCode = "connection_timeout"
			}
			t.Run(name, func(t *testing.T) {
				s, _ := testService(t)
				d := addMac(t, s, "offline_mac")
				d.WakeMethod = method
				d.WoL = model.WoL{MAC: "02:11:22:33:44:55", Broadcast: "255.255.255.255", Port: 9, Repeat: 1, IntervalMS: 100}
				if _, err := s.SaveDevice(d, nil, false, false); err != nil {
					t.Fatal(err)
				}
				// A stub prevents this test from ever sending a real LAN broadcast.
				s.SendWoL = func(context.Context, model.WoL) error { return nil }
				s.VNC = func(_ context.Context, o macvnc.Options) (macvnc.Evidence, error) {
					if o.RetryConnect != (method == "wol_mac_vnc") {
						t.Error("wrong connection retry policy")
					}
					remaining := time.Until(o.AuthDeadline)
					if remaining <= 25*time.Second || remaining > 30*time.Second {
						t.Errorf("unexpected shared budget: %v", remaining)
					}
					if o.RetryConnect {
						o.Progress("waiting_network")
					}
					return macvnc.Evidence{}, &macvnc.ConnectError{Err: cause}
				}
				j, err := s.Submit(context.Background(), command(d.ID, "offline_connection"))
				if err != nil {
					t.Fatal(err)
				}
				got := waitJob(t, s, j.ID)
				wantStatus := "failed"
				if method == "wol_mac_vnc" {
					wantStatus = "partial"
				}
				last := got.Steps[len(got.Steps)-1]
				if got.Status != wantStatus || got.Code != wantCode || last.Code != wantCode || !strings.Contains(last.Message, cause.Error()) || !strings.Contains(got.Message, cause.Error()) {
					t.Fatalf("lost TCP diagnosis: %+v", got)
				}
				s.mu.Lock()
				_, guarded := s.policy.Targets[d.TargetKey()]
				s.mu.Unlock()
				if guarded {
					t.Fatal("TCP failure incorrectly started an authentication cooldown")
				}
				// Network failures must allow another user-initiated request immediately.
				next, err := s.Submit(context.Background(), command(d.ID, "offline_retry"))
				if err != nil {
					t.Fatalf("TCP failure blocked a new request: %v", err)
				}
				waitJob(t, s, next.ID)
			})
		}
	}
}
