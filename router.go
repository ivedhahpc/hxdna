package hxdna

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
)

// Router dispatches incoming HxCommands to registered handlers.
// It is the worker's equivalent of Chi — one NATS subscriber, a handler registry, clean dispatch.
type Router struct {
	manifest Manifest
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRouter creates a Router for the given capability manifest.
// Built-in handlers for ping and describe_capabilities are registered automatically.
// Workers can override them by calling Register with the same key.
func NewRouter(m Manifest) *Router {
	r := &Router{
		manifest: m,
		handlers: make(map[string]Handler),
	}

	host, _ := os.Hostname()
	r.handlers["ping"] = func(_ HxCommand) (any, error) {
		return map[string]string{"pong": "ok", "host": host}, nil
	}
	r.handlers["describe_capabilities"] = func(_ HxCommand) (any, error) {
		return m, nil
	}

	return r
}

// Register adds a handler for the given command key.
// Safe to call concurrently with Serve. Register before Serve to ensure
// handlers are ready when the first command arrives.
func (r *Router) Register(key string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[key] = h
}

// ServeConfig holds everything the router needs to connect and announce.
type ServeConfig struct {
	State       *State
	ServiceName string // logged as "service"; defaults to "worker" if unset
	Version     string
	LogLevel    string
	LogMode     string
	Concurrency int // max simultaneous handlers; 0 defaults to 8
}

type capabilityCard struct {
	AgentID     string    `json:"agent_id"`
	OrgID       string    `json:"org_id"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Version     string    `json:"version"`
	AnnouncedAt time.Time `json:"announced_at"`
	Manifest    Manifest  `json:"manifest"`
}

// Serve checks enrollment status, connects to NATS, announces the capability card,
// and begins processing HxCommands. Blocks until SIGINT or SIGTERM.
func (r *Router) Serve(cfg ServeConfig) error {
	s := cfg.State

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "worker"
	}
	if err := SetupLogger(serviceName, cfg.Version, cfg.LogLevel, cfg.LogMode); err != nil {
		return fmt.Errorf("logger setup failed: %w", err)
	}
	log := L()

	status, err := CheckApproval(s)
	if err != nil {
		return err
	}
	switch status {
	case "pending":
		log.Infow("waiting for approval", "dashboard", s.ControlURL)
		return nil
	case "revoked":
		log.Infow("enrollment revoked — re-enroll to connect")
		return nil
	case "active":
		// proceed
	default:
		return fmt.Errorf("unknown status %q", status)
	}

	log.Infow("connecting", "nats_url", s.NatsURL, "worker_id", s.WorkerID)

	hostname, _ := os.Hostname()
	card := capabilityCard{
		AgentID:  s.WorkerID,
		OrgID:    s.OrgID,
		Hostname: hostname,
		OS:       runtime.GOOS + "/" + runtime.GOARCH,
		Version:  cfg.Version,
		Manifest: r.manifest,
	}
	onlineSubject := fmt.Sprintf("hx.agents.%s.%s.online", s.OrgID, s.WorkerID)

	announce := func(nc *nats.Conn) error {
		card.AnnouncedAt = time.Now().UTC()
		b, err := json.Marshal(card)
		if err != nil {
			return fmt.Errorf("failed to marshal capability card: %w", err)
		}
		if err := nc.Publish(onlineSubject, b); err != nil {
			return fmt.Errorf("failed to announce: %w", err)
		}
		return nc.FlushTimeout(5 * time.Second)
	}

	var disconnected atomic.Bool

	nc, err := nats.Connect(s.NatsURL,
		nats.Name("worker-"+s.WorkerID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(5*time.Second),
		nats.DrainTimeout(30*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if disconnected.CompareAndSwap(false, true) {
				log.Warnw("connection lost — waiting to reconnect", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			disconnected.Store(false)
			log.Infow("reconnected")
			if err := announce(nc); err != nil {
				log.Errorw("re-announce failed", "error", err)
			} else {
				log.Infow("re-announced", "subject", onlineSubject)
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("NATS connect failed: %w", err)
	}
	defer nc.Close()

	if err := announce(nc); err != nil {
		return err
	}
	log.Infow("announced", "subject", onlineSubject)

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 8
	}
	sem := make(chan struct{}, concurrency)

	cmdSubject := fmt.Sprintf("hx.agents.%s.%s.cmd.>", s.OrgID, s.WorkerID)

	_, err = nc.Subscribe(cmdSubject, func(msg *nats.Msg) {
		var cmd HxCommand
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			log.Errorw("bad payload", "error", err)
			if msg.Reply != "" {
				result := HxResult{
					Success:    false,
					Error:      "bad payload: " + err.Error(),
					ExecutedAt: time.Now().UTC(),
				}
				_ = nc.Publish(msg.Reply, result.ToJSON())
			}
			return
		}

		replySubject := msg.Reply
		if replySubject == "" {
			if cmd.RequestID == "" {
				log.Warnw("dropping result: no reply subject and no request_id", "command", cmd.CommandKey)
				return
			}
			replySubject = fmt.Sprintf("hx.agents.%s.%s.result.%s", s.OrgID, s.WorkerID, cmd.RequestID)
		}

		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()

			start := time.Now()
			log.Debugw("command received", "command", cmd.CommandKey, "ticket_id", cmd.TicketID, "resource_id", cmd.ResourceID)

			result := r.dispatch(cmd)

			elapsed := time.Since(start).Round(time.Millisecond)
			if result.Success {
				log.Infow("command ok", "command", cmd.CommandKey, "elapsed", elapsed)
			} else {
				log.Errorw("command failed", "command", cmd.CommandKey, "error", result.Error, "elapsed", elapsed)
			}

			if err := nc.Publish(replySubject, result.ToJSON()); err != nil {
				log.Errorw("failed to publish result", "error", err)
			}
		}()
	})
	if err != nil {
		return fmt.Errorf("subscribe failed: %w", err)
	}

	log.Infow("listening", "subject", cmdSubject)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	sig := <-quit
	log.Infow("shutting down", "signal", sig)
	return nc.Drain()
}

// dispatch routes an HxCommand to the registered handler and returns the result.
func (r *Router) dispatch(cmd HxCommand) HxResult {
	result := HxResult{
		RequestID:  cmd.RequestID,
		ExecutedAt: time.Now().UTC(),
	}

	r.mu.RLock()
	handler := r.handlers[cmd.CommandKey]
	r.mu.RUnlock()

	if handler == nil {
		result.Success = false
		result.Error = fmt.Sprintf("unknown command: %s", cmd.CommandKey)
		return result
	}

	data, err := handler(cmd)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
		if data != nil {
			b, marshalErr := json.Marshal(data)
			if marshalErr != nil {
				result.Success = false
				result.Error = "marshal result data: " + marshalErr.Error()
				return result
			}
			result.Data = b
		}
	}
	return result
}
