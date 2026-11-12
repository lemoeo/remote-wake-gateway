package app

import "github.com/lemoeo/remote-wake-gateway/internal/model"

const maxEventSubscribers = 16
const eventBufferSize = 32

// Events are transient notifications. Persisted tasks remain the source of
// truth; subscribers must resynchronize after every connection/reconnection.
type Event struct {
	Type     string          `json:"-"`
	Job      *model.Job      `json:"job,omitempty"`
	Message  string          `json:"message,omitempty"`
	Revision int             `json:"revision,omitempty"`
	Cleanup  *HistoryCleanup `json:"cleanup,omitempty"`
}

func (s *Service) Subscribe() (<-chan Event, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.eventsClosed {
		return nil, nil, model.Error(503, "service_stopping", "服务正在停止")
	}
	if len(s.subscribers) >= maxEventSubscribers {
		return nil, nil, model.Error(429, "stream_limit", "实时连接过多，请关闭其他管理页面后重试")
	}
	if s.subscribers == nil {
		s.subscribers = make(map[chan Event]struct{})
	}
	ch := make(chan Event, eventBufferSize)
	s.subscribers[ch] = struct{}{}
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
	}
	return ch, cancel, nil
}

// Called only while the application mutex is held. Never wait on a browser.
func (s *Service) emitLocked(event Event) {
	for ch := range s.subscribers {
		select {
		case ch <- event:
		default:
			delete(s.subscribers, ch)
			close(ch)
		}
	}
}

func (s *Service) jobEventLocked(j *model.Job) {
	copy := j.Clone()
	s.emitLocked(Event{Type: "job.updated", Job: &copy})
}

func (s *Service) storageFailedLocked(err error) {
	s.storageErr = err
	s.emitLocked(Event{Type: "storage.error", Message: "存储写入失败，已暂停接受任务，请检查磁盘并重启服务"})
}

func (s *Service) devicesEventLocked() {
	s.emitLocked(Event{Type: "devices.changed", Revision: s.devices.Revision})
}

func (s *Service) closeStreamsLocked() {
	s.eventsClosed = true
	for ch := range s.subscribers {
		delete(s.subscribers, ch)
		close(ch)
	}
}

// CloseStreams releases long-lived HTTP requests before Server.Shutdown waits
// for them. Workers are still shut down separately by Close.
func (s *Service) CloseStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeStreamsLocked()
}
