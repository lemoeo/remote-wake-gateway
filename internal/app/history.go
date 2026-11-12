package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

// Before freezes the preview's boundary, so confirmation never includes tasks
// which finished or left the command protection window after the preview.
type HistoryCleanup struct {
	Scope         string    `json:"scope"`
	Before        time.Time `json:"before"`
	RetentionDays int       `json:"retention_days"`
	Eligible      int       `json:"eligible"`
	Active        int       `json:"active"`
	Protected     int       `json:"protected"`
	Deleted       int       `json:"deleted"`
}

func validCleanupScope(scope string) bool { return scope == "expired" || scope == "completed" }

func (s *Service) PreviewHistoryCleanup(scope string) (HistoryCleanup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validCleanupScope(scope) {
		return HistoryCleanup{}, model.Error(422, "invalid_cleanup_scope", "请选择过期记录或全部已结束记录")
	}
	if err := s.writable(); err != nil {
		return HistoryCleanup{}, err
	}
	plan, _ := s.historyCleanupLocked(scope, time.Now().UTC())
	return plan, nil
}

func (s *Service) historyCleanupLocked(scope string, before time.Time) (HistoryCleanup, []string) {
	plan := HistoryCleanup{Scope: scope, Before: before, RetentionDays: s.cfg.RetentionDays}
	var ids []string
	for id, j := range s.jobs {
		if j.Active() {
			plan.Active++
			continue
		}
		// Keep the same replay protection window as automatic retention cleanup.
		if !j.ExpiresAt.Before(before.Add(-time.Minute)) {
			plan.Protected++
			continue
		}
		if j.FinishedAt.After(before) {
			continue
		}
		if scope == "expired" && !j.FinishedAt.Before(before.Add(-time.Duration(plan.RetentionDays)*24*time.Hour)) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	plan.Eligible = len(ids)
	return plan, ids
}

func (s *Service) CleanupHistory(ctx context.Context, scope string, before time.Time) (HistoryCleanup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validCleanupScope(scope) || before.IsZero() || before.After(time.Now()) {
		return HistoryCleanup{}, model.Error(422, "invalid_cleanup_request", "清理范围或预览时间无效，请重新预览")
	}
	if err := s.writable(); err != nil {
		return HistoryCleanup{}, err
	}
	plan, ids := s.historyCleanupLocked(scope, before)
	// A partial filesystem failure must still invalidate other pages' snapshots.
	complete := false
	defer func() {
		if plan.Deleted == 0 {
			return
		}
		if complete {
			s.emitLocked(Event{Type: "history.cleared", Cleanup: &plan})
		} else {
			s.emitLocked(Event{Type: "history.invalidated"})
		}
	}()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return plan, model.Error(408, "cleanup_interrupted", fmt.Sprintf("清理已中断，已删除 %d 条记录，请重新预览", plan.Deleted))
		}
		if err := os.Remove(s.fs.Path(filepath.Join("jobs", id+".json"))); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.storageFailedLocked(err)
			return plan, model.Error(503, "storage_unavailable", fmt.Sprintf("清理失败，已删除 %d 条记录，请检查存储并重启服务", plan.Deleted))
		}
		delete(s.commands, s.jobs[id].CommandID)
		delete(s.jobs, id)
		plan.Deleted++
	}
	complete = true
	return plan, nil
}
