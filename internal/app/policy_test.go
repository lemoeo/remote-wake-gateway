package app

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
)

func guardSnapshot(s *Service) map[string]Guard {
	s.mu.Lock()
	defer s.mu.Unlock()
	guards := make(map[string]Guard, len(s.policy.Targets))
	for key, guard := range s.policy.Targets {
		guards[key] = guard
	}
	return guards
}

func assertGuards(t *testing.T, got, want map[string]Guard) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("guard count = %d, want %d: %+v", len(got), len(want), got)
	}
	for key, expected := range want {
		actual, ok := got[key]
		if !ok || actual.Blocked != expected.Blocked || actual.InFlight != expected.InFlight || !actual.Until.Equal(expected.Until) {
			t.Fatalf("guard %q = %+v (exists %t), want %+v", key, actual, ok, expected)
		}
	}
}

func TestExpiredGuardPruningPreservesProtectionAndActiveTargets(t *testing.T) {
	now := time.Now().UTC()
	kept := map[string]Guard{
		"future":  {Until: now.Add(time.Nanosecond)},
		"blocked": {Until: now.Add(-time.Hour), Blocked: true},
		"flight":  {Until: now.Add(-time.Hour), InFlight: true},
		"active":  {Until: now.Add(-time.Hour)},
	}
	s := &Service{policy: Policy{Targets: map[string]Guard{
		"expired": {Until: now.Add(-time.Hour)},
		"at_end":  {Until: now},
		"empty":   {},
	}}, targets: map[string]string{"active": "job_active"}}
	for key, guard := range kept {
		s.policy.Targets[key] = guard
	}
	if !s.pruneExpiredGuardsLocked(now) {
		t.Fatal("expired cooldowns were retained")
	}
	assertGuards(t, s.policy.Targets, kept)
	if s.pruneExpiredGuardsLocked(now) {
		t.Fatal("cleanup changed live protection")
	}
	delete(s.targets, "active")
	if !s.pruneExpiredGuardsLocked(now) {
		t.Fatal("released task's expired record was retained")
	}
	delete(kept, "active")
	assertGuards(t, s.policy.Targets, kept)
}

func TestStartupPrunesExpiredGuardsAfterRecoveringInterruptedAuthentication(t *testing.T) {
	s, fs := testService(t)
	s.Close()
	now := time.Now().UTC()
	policy := Policy{SchemaVersion: s.policy.SchemaVersion, Targets: map[string]Guard{
		"old_deleted_target": {Until: now.Add(-time.Hour)},
		"empty":              {},
		"future":             {Until: now.Add(time.Hour)},
		"blocked":            {Until: now.Add(-time.Hour), Blocked: true},
		"interrupted":        {Until: now.Add(-time.Hour), InFlight: true},
	}}
	if err := fs.Write("runtime-policy.json", policy); err != nil {
		t.Fatal(err)
	}
	next, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	recovered := next.policy.Targets["interrupted"]
	if recovered.InFlight || recovered.Blocked || recovered.Until.Before(now.Add(time.Minute)) {
		t.Fatalf("interrupted authentication lost its recovery cooldown: %+v", recovered)
	}
	want := map[string]Guard{"future": policy.Targets["future"], "blocked": policy.Targets["blocked"], "interrupted": recovered}
	assertGuards(t, next.policy.Targets, want)
	var persisted Policy
	if err := fs.Read("runtime-policy.json", &persisted); err != nil {
		t.Fatal(err)
	}
	assertGuards(t, persisted.Targets, want)
}

func TestVNCSubmissionPrunesOldTargetsAndKeepsFailureCooldown(t *testing.T) {
	s, fs := testService(t)
	d := addMac(t, s, "prune_guards")
	now := time.Now().UTC()
	policy := Policy{SchemaVersion: s.policy.SchemaVersion, Targets: map[string]Guard{
		"future":      {Until: now.Add(time.Hour)},
		"blocked":     {Until: now.Add(-time.Hour), Blocked: true},
		d.TargetKey(): {Until: now.Add(-time.Hour)},
	}}
	for i := range 500 {
		policy.Targets[fmt.Sprintf("old_target_%d", i)] = Guard{Until: now.Add(-time.Hour)}
	}
	s.policy = policy
	if err := fs.Write("runtime-policy.json", policy); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.VNC = func(_ context.Context, options macvnc.Options) (macvnc.Evidence, error) {
		calls.Add(1)
		if err := options.BeforeAuth(); err != nil {
			return macvnc.Evidence{}, err
		}
		return macvnc.Evidence{AuthSent: true}, &macvnc.Failure{Code: "auth_rejected"}
	}
	j, err := s.Submit(context.Background(), command(d.ID, "after_pruning"))
	if err != nil {
		t.Fatal(err)
	}
	if result := waitJob(t, s, j.ID); result.Status != "failed" {
		t.Fatalf("expected authentication failure, got %+v", result)
	}
	guards := guardSnapshot(s)
	cooldown := guards[d.TargetKey()]
	if cooldown.InFlight || cooldown.Blocked || cooldown.Until.Before(now.Add(time.Minute)) {
		t.Fatalf("new failure did not retain its 60s cooldown: %+v", cooldown)
	}
	want := map[string]Guard{"future": policy.Targets["future"], "blocked": policy.Targets["blocked"], d.TargetKey(): cooldown}
	assertGuards(t, guards, want)
	for i := range 2 {
		if _, err := s.Submit(context.Background(), command(d.ID, fmt.Sprintf("during_cooldown_%d", i))); code(err) != "authentication_cooldown" {
			t.Fatalf("active cooldown was bypassed: %v", err)
		}
	}
	var persisted Policy
	if err := fs.Read("runtime-policy.json", &persisted); err != nil {
		t.Fatal(err)
	}
	assertGuards(t, persisted.Targets, want)
	if calls.Load() != 1 {
		t.Fatalf("authentication retried during cooldown: %d", calls.Load())
	}
}

func TestAutomaticGuardCleanupDoesNotClearExplicitBlockAcrossRestart(t *testing.T) {
	s, fs := testService(t)
	d := addMac(t, s, "blocked_guard")
	blocked := Guard{Until: time.Now().Add(-time.Hour), Blocked: true}
	s.policy.Targets = map[string]Guard{d.TargetKey(): blocked, "expired": {Until: time.Now().Add(-time.Hour)}}
	if err := fs.Write("runtime-policy.json", s.policy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(context.Background(), command(d.ID, "blocked_after_cleanup")); code(err) != "authentication_cooldown" {
		t.Fatalf("explicit block was bypassed: %v", err)
	}
	want := map[string]Guard{d.TargetKey(): blocked}
	assertGuards(t, s.policy.Targets, want)
	s.Close()
	next, err := New(fs)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, err := next.Submit(context.Background(), command(d.ID, "blocked_after_restart")); code(err) != "authentication_cooldown" {
		t.Fatalf("restart removed explicit block: %v", err)
	}
	assertGuards(t, next.policy.Targets, want)
	var persisted Policy
	if err := fs.Read("runtime-policy.json", &persisted); err != nil {
		t.Fatal(err)
	}
	assertGuards(t, persisted.Targets, want)
}

func TestGuardCleanupPersistenceFailureStopsNewAuthentication(t *testing.T) {
	s, fs := testService(t)
	d := addMac(t, s, "guard_storage_failure")
	blocked := Guard{Blocked: true}
	s.policy.Targets = map[string]Guard{"blocked": blocked, "expired": {Until: time.Now().Add(-time.Hour)}}
	if err := fs.Write("runtime-policy.json", s.policy); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fs.Path("runtime-policy.json"), fs.Path("runtime-policy.saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fs.Path("runtime-policy.json"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.VNC = func(context.Context, macvnc.Options) (macvnc.Evidence, error) {
		calls.Add(1)
		return macvnc.Evidence{Authenticated: true}, nil
	}
	if _, err := s.Submit(context.Background(), command(d.ID, "failed_guard_cleanup")); code(err) != "storage_unavailable" {
		t.Fatalf("cleanup persistence failure was ignored: %v", err)
	}
	if s.Ready() || len(s.Jobs()) != 0 || calls.Load() != 0 {
		t.Fatal("authentication accepted despite policy persistence failure")
	}
	assertGuards(t, s.policy.Targets, map[string]Guard{"blocked": blocked})
}
