package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
	"github.com/lemoeo/remote-wake-gateway/internal/storage"
)

func TestJobPagesAreStableAcrossTiesFilteringAndDeletedAnchor(t *testing.T) {
	s, _ := testService(t)
	now := time.Now().UTC()
	for i := range 120 {
		j := seedHistory(t, s, fmt.Sprintf("job_%03d", i), "success", now.Add(-time.Hour), now.Add(-time.Hour))
		s.mu.Lock()
		stored := s.jobs[j.ID]
		stored.CreatedAt = now
		stored.DeviceID = fmt.Sprintf("device_%d", i%3)
		if i%2 == 0 {
			stored.Status = "failed"
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.rebuildJobOrderLocked()
	s.mu.Unlock()
	first, err := s.ListJobs(JobQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 50 || first.NextCursor == "" || first.Jobs[0].ID != "job_119" {
		t.Fatalf("first page: %+v", first)
	}
	anchor := first.Jobs[49].ID
	s.mu.Lock()
	delete(s.jobs, anchor)
	s.compactJobOrderLocked()
	s.mu.Unlock()
	second, err := s.ListJobs(JobQuery{Limit: 50, After: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 50 || second.Jobs[0].ID != "job_069" {
		t.Fatalf("deleted cursor skipped records: %+v", second)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.ListJobs(JobQuery{Limit: 7, After: cursor, DeviceID: "device_0", Status: "failed"})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range page.Jobs {
			if seen[j.ID] || j.DeviceID != "device_0" || j.Status != "failed" {
				t.Fatal("page duplicated or violated filters")
			}
			seen[j.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 20 {
		t.Fatalf("filtered count: %d", len(seen))
	}
	legacy, err := s.ListJobs(JobQuery{Limit: 2, After: "job_119"})
	if err != nil || len(legacy.Jobs) != 2 || legacy.Jobs[0].ID != "job_118" {
		t.Fatal("legacy cursor not supported")
	}
	if _, err = s.ListJobs(JobQuery{Limit: 50, After: anchor}); code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	for _, query := range []JobQuery{{Limit: 0}, {Limit: 101}, {Limit: 50, After: "v1.not-json"}} {
		if _, err = s.ListJobs(query); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
}

func benchmarkHistory(b *testing.B) *Service {
	b.Helper()
	fs, err := storage.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	s, err := New(fs)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close(); fs.Close() })
	now := time.Now().UTC()
	for i := range 100000 {
		id := fmt.Sprintf("job_%06d", i)
		s.jobs[id] = &model.Job{ID: id, CreatedAt: now.Add(time.Duration(i) * time.Millisecond), Status: "success", Steps: []model.Step{{Name: "wol", Status: "success"}}}
	}
	s.rebuildJobOrderLocked()
	return s
}
func BenchmarkHistoryPage100000(b *testing.B) {
	s := benchmarkHistory(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := s.ListJobs(JobQuery{Limit: 50})
		if err != nil || len(page.Jobs) != 50 {
			b.Fatal(err)
		}
	}
}
func BenchmarkHistoryFullSnapshot100000(b *testing.B) {
	s := benchmarkHistory(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(s.Jobs()) != 100000 {
			b.Fatal("snapshot length")
		}
	}
}
