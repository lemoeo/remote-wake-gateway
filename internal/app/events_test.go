package app

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/macvnc"
	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

func nextEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("event not delivered")
		return Event{}
	}
}

func TestEventSubscriberLimitsOverflowAndShutdown(t *testing.T) {
	s, _ := testService(t)
	var streams []<-chan Event
	for range maxEventSubscribers {
		stream, cancel, err := s.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cancel)
		streams = append(streams, stream)
	}
	if _, _, err := s.Subscribe(); code(err) != "stream_limit" {
		t.Fatalf("limit: %v", err)
	}
	s.mu.Lock()
	for range eventBufferSize + 1 {
		s.emitLocked(Event{Type: "test"})
	}
	remaining := len(s.subscribers)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("slow subscribers retained")
	}
	for _, stream := range streams {
		count := 0
		for range stream {
			count++
		}
		if count != eventBufferSize {
			t.Fatalf("buffer grew: %d", count)
		}
	}
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cancel()
	if _, ok := <-stream; ok {
		t.Fatal("unsubscribe did not close")
	}
	stream, _, err = s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	s.CloseStreams()
	if _, ok := <-stream; ok {
		t.Fatal("shutdown retained stream")
	}
	if _, _, err := s.Subscribe(); code(err) != "service_stopping" {
		t.Fatal(err)
	}
}

func TestJobEventsAreSnapshotsAndTerminalStateIsPersisted(t *testing.T) {
	s, fs := testService(t)
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	d := addMac(t, s, "events_mac")
	if e := nextEvent(t, stream); e.Type != "devices.changed" || e.Revision != s.Devices().Revision {
		t.Fatalf("device event: %+v", e)
	}
	s.VNC = func(context.Context, macvnc.Options) (macvnc.Evidence, error) {
		return macvnc.Evidence{Connected: true, Authenticated: true}, nil
	}
	j, err := s.Submit(context.Background(), command(d.ID, "events_command"))
	if err != nil {
		t.Fatal(err)
	}
	first := nextEvent(t, stream)
	if first.Type != "job.updated" || first.Job.Status != "pending" {
		t.Fatalf("first job event: %+v", first)
	}
	var stepSnapshot *model.Job
	for {
		e := nextEvent(t, stream)
		if e.Job == nil {
			continue
		}
		if len(e.Job.Steps) > 0 && e.Job.Steps[0].Status == "running" {
			stepSnapshot = e.Job
		}
		if e.Job.Active() {
			continue
		}
		if e.Job.ID != j.ID || e.Job.Status != "success" {
			t.Fatalf("terminal event: %+v", e.Job)
		}
		var saved model.Job
		if err := fs.Read("jobs/"+j.ID+".json", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Status != e.Job.Status {
			t.Fatal("terminal event preceded persistence")
		}
		break
	}
	if first.Job.Status != "pending" || stepSnapshot == nil || stepSnapshot.Steps[0].Status != "running" {
		t.Fatal("queued snapshots changed in place")
	}
}

func TestTerminalPersistenceFailureDoesNotEmitSuccess(t *testing.T) {
	s, fs := testService(t)
	d := addMac(t, s, "failed_events")
	entered, release := make(chan struct{}), make(chan struct{})
	s.VNC = func(ctx context.Context, _ macvnc.Options) (macvnc.Evidence, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return macvnc.Evidence{}, ctx.Err()
		}
		return macvnc.Evidence{Authenticated: true}, nil
	}
	stream, cancel, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_, err = s.Submit(context.Background(), command(d.ID, "failed_events_command"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = os.Rename(fs.Path("jobs"), fs.Path("jobs.saved")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fs.Path("jobs"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	close(release)
	sawStorage := false
	for {
		e := nextEvent(t, stream)
		if e.Type == "storage.error" {
			sawStorage = true
		}
		if e.Job == nil {
			continue
		}
		if !e.Job.Active() {
			t.Fatalf("unpersisted terminal event: %+v", e.Job)
		}
		if e.Job.Code == "storage_unavailable" {
			break
		}
	}
	if !sawStorage || s.Ready() {
		t.Fatal("storage failure not surfaced")
	}
}
