package httptransport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/lemoeo/remote-wake-gateway/internal/model"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r) {
		writeError(w, model.Error(403, "invalid_origin", "请求来源不允许"))
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, model.Error(501, "stream_unavailable", "当前 HTTP 服务不支持实时推送"))
		return
	}
	ch, unsubscribe, err := s.App.Subscribe()
	if err != nil {
		writeError(w, err)
		return
	}
	defer unsubscribe()
	controller := http.NewResponseController(w)
	// Only this streaming response is exempt from the normal 15s write timeout.
	if err = controller.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, model.Error(501, "stream_unavailable", "当前 HTTP 服务不支持实时推送超时控制"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(kind string, data any) error {
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if err = controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		defer controller.SetWriteDeadline(time.Time{})
		if kind == "" {
			_, err = fmt.Fprint(w, ": heartbeat\n\n")
		} else {
			_, err = fmt.Fprintf(w, "retry: 3000\nevent: %s\ndata: %s\n\n", kind, body)
		}
		if err != nil {
			return err
		}
		return controller.Flush()
	}
	// No event replay or historical jobs. A newly subscribed browser reconciles
	// devices and its known/active task IDs while this queue captures new changes.
	if write("resync", map[string]bool{"ready": s.App.Ready()}) != nil {
		return
	}
	heartbeat := time.NewTicker(s.streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if _, ok := s.Auth.Session(r); !ok {
				_ = write("auth.expired", struct{}{})
				return
			}
			if write(event.Type, event) != nil {
				return
			}
		case <-heartbeat.C:
			if _, ok := s.Auth.Session(r); !ok {
				_ = write("auth.expired", struct{}{})
				return
			}
			if write("", nil) != nil {
				return
			}
		}
	}
}
