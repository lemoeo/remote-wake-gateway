package app

import "time"

// Called with the application mutex held, or during startup before workers run.
// Expired cooldowns no longer protect an authentication attempt. Explicit blocks
// and records still owned by a running task must survive automatic cleanup.
func (s *Service) pruneExpiredGuardsLocked(now time.Time) bool {
	changed := false
	for target, guard := range s.policy.Targets {
		if guard.Blocked || guard.InFlight || guard.Until.After(now) || s.targets[target] != "" {
			continue
		}
		delete(s.policy.Targets, target)
		changed = true
	}
	if changed {
		// Rebuild after pruning so a large historical map can release its capacity.
		kept := make(map[string]Guard, len(s.policy.Targets))
		for target, guard := range s.policy.Targets {
			kept[target] = guard
		}
		s.policy.Targets = kept
	}
	return changed
}
