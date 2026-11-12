package app

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

type jobKey struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}
type JobQuery struct {
	Limit                   int
	After, DeviceID, Status string
}
type JobPage struct {
	Jobs       []model.Job `json:"jobs"`
	NextCursor string      `json:"next_cursor"`
}

func keyBefore(a, b jobKey) bool {
	return a.CreatedAt.After(b.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && a.ID > b.ID
}
func (s *Service) rebuildJobOrderLocked() {
	s.jobOrder = make([]jobKey, 0, len(s.jobs))
	for id, j := range s.jobs {
		s.jobOrder = append(s.jobOrder, jobKey{id, j.CreatedAt})
	}
	sort.Slice(s.jobOrder, func(i, j int) bool { return keyBefore(s.jobOrder[i], s.jobOrder[j]) })
}
func (s *Service) indexJobLocked(j model.Job) {
	key := jobKey{j.ID, j.CreatedAt}
	i := sort.Search(len(s.jobOrder), func(i int) bool { return !keyBefore(s.jobOrder[i], key) })
	s.jobOrder = append(s.jobOrder, jobKey{})
	copy(s.jobOrder[i+1:], s.jobOrder[i:])
	s.jobOrder[i] = key
}
func (s *Service) compactJobOrderLocked() {
	kept := s.jobOrder[:0]
	for _, key := range s.jobOrder {
		if s.jobs[key.ID] != nil {
			kept = append(kept, key)
		}
	}
	clear(s.jobOrder[len(kept):])
	s.jobOrder = kept
}

// Copy only this page's records while holding the mutex. The immutable index
// keys also let a cursor survive removal of its anchor record.
func (s *Service) ListJobs(q JobQuery) (JobPage, error) {
	page := JobPage{Jobs: []model.Job{}}
	if q.Limit < 1 || q.Limit > 100 {
		return page, model.Error(422, "invalid_limit", "limit 须为 1–100")
	}
	var cursor jobKey
	legacy := q.After != "" && !strings.HasPrefix(q.After, "v1.")
	if q.After != "" && !legacy {
		if len(q.After) > 512 {
			return page, model.Error(422, "invalid_cursor", "分页标识无效，请刷新记录")
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(q.After, "v1."))
		if err != nil || json.Unmarshal(raw, &cursor) != nil || !model.ValidID(cursor.ID) || cursor.CreatedAt.IsZero() {
			return page, model.Error(422, "invalid_cursor", "分页标识无效，请刷新记录")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if legacy {
		j := s.jobs[q.After]
		if j == nil {
			return page, model.Error(409, "cursor_expired", "分页对应的记录已清理，请刷新记录")
		}
		cursor = jobKey{j.ID, j.CreatedAt}
	}
	start := 0
	if q.After != "" {
		start = sort.Search(len(s.jobOrder), func(i int) bool { return keyBefore(cursor, s.jobOrder[i]) })
	}
	for _, key := range s.jobOrder[start:] {
		j := s.jobs[key.ID]
		if j == nil || q.DeviceID != "" && j.DeviceID != q.DeviceID || q.Status != "" && j.Status != q.Status {
			continue
		}
		if len(page.Jobs) == q.Limit {
			last := page.Jobs[len(page.Jobs)-1]
			raw, _ := json.Marshal(jobKey{last.ID, last.CreatedAt})
			page.NextCursor = "v1." + base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		page.Jobs = append(page.Jobs, j.Clone())
	}
	return page, nil
}
