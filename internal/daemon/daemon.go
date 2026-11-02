package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"go-aws/internal/ipc"
	"go-aws/internal/model"
	"go-aws/internal/platform"
)

// ErrDaemonRunning is returned when another daemon holds the single-instance
// lock.
var ErrDaemonRunning = errors.New("daemon already running")

// Hello is the handshake response.
type Hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
}

// Run starts the daemon server and blocks until shutdown.
func Run(ctx context.Context, opts Options) error {
	opts.normalize()
	lockPath, err := platform.LockPath()
	if err != nil {
		return err
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock daemon: %w", err)
	}
	if !locked {
		return ErrDaemonRunning
	}
	defer func() {
		_ = lock.Unlock()
		_ = lock.Close()
	}()

	CleanupStale()

	mgr := NewManager(opts)
	h := &serverHandler{
		mgr:     mgr,
		opts:    opts,
		started: time.Now().UTC(),
		stopCh:  make(chan struct{}),
	}
	srv, err := ipc.NewServer(h.handle)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			return ErrDaemonRunning
		}
		return fmt.Errorf("listen on daemon endpoint: %w", err)
	}
	h.srv = srv
	mgr.SetEmitter(srv.Broadcast)

	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(serveCtx) }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var idleSince time.Time
	shutdown := func() {
		srv.Broadcast(ipc.Event{Event: "daemon.shutting_down", Message: "daemon is shutting down"})
		mgr.Shutdown(6 * time.Second)
		srv.Close()
	}
	defer shutdown()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.stopCh:
			return nil
		case err := <-serveErr:
			if err != nil {
				return err
			}
			return nil
		case now := <-ticker.C:
			if mgr.Count() == 0 && srv.ClientCount() == 0 {
				if idleSince.IsZero() {
					idleSince = now
				} else if now.Sub(idleSince) >= opts.IdleExit {
					return nil
				}
			} else {
				idleSince = time.Time{}
			}
		}
	}
}

type serverHandler struct {
	mgr     *Manager
	opts    Options
	started time.Time
	srv     *ipc.Server
	stopCh  chan struct{}
	stopOne sync.Once
}

func (h *serverHandler) requestStop() {
	h.stopOne.Do(func() {
		go func() {
			time.Sleep(75 * time.Millisecond)
			close(h.stopCh)
		}()
	})
}

func (h *serverHandler) info() model.DaemonInfo {
	info := model.DaemonInfo{
		Version:       h.opts.Version,
		Protocol:      ipc.ProtocolVersion,
		StartedAt:     h.started,
		Connections:   h.mgr.Count(),
		IdleExitAfter: h.opts.IdleExit.String(),
	}
	if h.srv != nil {
		info.Clients = h.srv.ClientCount()
	}
	return info
}

func (h *serverHandler) handle(ctx context.Context, _ *ipc.ServerClient, method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		return Hello{Protocol: ipc.ProtocolVersion, Version: h.opts.Version}, nil
	case "ping":
		return "pong", nil
	case "subscribe":
		return h.info(), nil
	case "daemon_info":
		return h.info(), nil
	case "list_connections":
		return h.mgr.List(), nil
	case "connect":
		var p ConnectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ipc.RPCError{Code: "invalid", Message: "invalid connect params: " + err.Error()}
		}
		return h.mgr.Connect(p)
	case "reopen_rdp":
		var p ReopenParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ipc.RPCError{Code: "invalid", Message: "invalid reopen params: " + err.Error()}
		}
		return h.mgr.Reopen(p.ID)
	case "close_connection":
		var p CloseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ipc.RPCError{Code: "invalid", Message: "invalid close params: " + err.Error()}
		}
		if p.All {
			h.mgr.CloseAll("closed by user")
			return map[string]bool{"ok": true}, nil
		}
		if p.ID == "" {
			return nil, &ipc.RPCError{Code: "invalid", Message: "connection id is required"}
		}
		if err := h.mgr.Close(p.ID, "closed by user"); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "daemon_stop":
		h.mgr.CloseAll("daemon stopping")
		h.requestStop()
		return map[string]bool{"ok": true}, nil
	default:
		return nil, &ipc.RPCError{Code: "method", Message: fmt.Sprintf("unknown method %q", method)}
	}
}

// SortedIDs is a helper for CLI output and tests.
func SortedIDs(conns []model.Connection) []string {
	ids := make([]string, len(conns))
	for i, c := range conns {
		ids[i] = c.ID
	}
	sort.Strings(ids)
	return ids
}
