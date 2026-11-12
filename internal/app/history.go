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
	plan, _ := s.historyCleanupLocked(scope, time.Now().UTC(), 0)
	return plan, nil
}

// limit=0 counts only; a positive limit bounds automatic maintenance; -1
// captures the full explicit cleanup selection. No sorting or file IO here.
func (s *Service) historyCleanupLocked(scope string, before time.Time, limit int) (HistoryCleanup, []string) {
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
		plan.Eligible++
		if limit != 0 {
			ids = append(ids, id)
		}
		if limit > 0 && len(ids) == limit {
			break
		}
	}
	return plan, ids
}

func (s *Service) CleanupHistory(ctx context.Context, scope string, before time.Time) (HistoryCleanup, error) {
	if !validCleanupScope(scope) || before.IsZero() || before.After(time.Now()) {
		return HistoryCleanup{}, model.Error(422, "invalid_cleanup_request", "清理范围或预览时间无效，请重新预览")
	}
	if !s.cleanupMu.TryLock() {
		return HistoryCleanup{}, model.Error(409, "cleanup_busy", "已有清理正在进行，请稍后重新预览")
	}
	defer s.cleanupMu.Unlock()
	s.mu.Lock()
	if err := s.writable(); err != nil {
		s.mu.Unlock()
		return HistoryCleanup{}, err
	}
	s.wg.Add(1)
	plan, ids := s.historyCleanupLocked(scope, before, -1)
	s.mu.Unlock()
	defer s.wg.Done()
	sort.Strings(ids)
	return s.deleteHistory(ctx, plan, ids, false)
}

const historySweepBatch = 128

func (s *Service) cleanupExpired(ctx context.Context) error {
	if !s.cleanupMu.TryLock() {
		return nil
	}
	defer s.cleanupMu.Unlock()
	s.mu.Lock()
	if err := s.writable(); err != nil {
		s.mu.Unlock()
		return err
	}
	now := time.Now().UTC()
	if now.Before(s.nextHistorySweep) && len(s.jobs) < s.cfg.MaxJobs {
		s.mu.Unlock()
		return nil
	}
	plan, ids := s.historyCleanupLocked("expired", now, historySweepBatch)
	if len(ids) < historySweepBatch {
		s.nextHistorySweep = now.Add(time.Minute)
	}
	if len(ids) == 0 {
		s.mu.Unlock()
		return nil
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	_, err := s.deleteHistory(ctx, plan, ids, true)
	return err
}

func (s *Service) deleteHistory(ctx context.Context, plan HistoryCleanup, ids []string, automatic bool) (HistoryCleanup, error) {
	// Candidates are terminal snapshots and cannot become active again. Serialize
	// file deletion separately, allowing readers and workers to use the app mutex.
	complete := false
	var removed []string
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if plan.Deleted == 0 {
			return
		}
		s.compactJobOrderLocked()
		if automatic {
			s.emitLocked(Event{Type: "history.removed", JobIDs: removed})
		} else if complete {
			s.emitLocked(Event{Type: "history.cleared", Cleanup: &plan})
		} else {
			s.emitLocked(Event{Type: "history.invalidated"})
		}
	}()
	for _, id := range ids {
		if ctx.Err() != nil || s.ctx.Err() != nil {
			return plan, model.Error(408, "cleanup_interrupted", fmt.Sprintf("清理已中断，已删除 %d 条记录，请重新预览", plan.Deleted))
		}
		if err := s.removeJobFile(s.fs.Path(filepath.Join("jobs", id+".json"))); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.mu.Lock()
			s.storageFailedLocked(err)
			s.mu.Unlock()
			return plan, model.Error(503, "storage_unavailable", fmt.Sprintf("清理失败，已删除 %d 条记录，请检查存储并重启服务", plan.Deleted))
		}
		s.mu.Lock()
		if j := s.jobs[id]; j != nil {
			if s.commands[j.CommandID] == id {
				delete(s.commands, j.CommandID)
			}
			delete(s.jobs, id)
		}
		s.mu.Unlock()
		removed = append(removed, id)
		plan.Deleted++
	}
	complete = true
	return plan, nil
}
