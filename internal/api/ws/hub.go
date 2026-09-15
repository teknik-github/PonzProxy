// Package ws broadcasts live metrics to every connected dashboard.
//
// The hub polls the collector once per tick and fans the result out, rather
// than letting each client poll independently: one snapshot serves every
// viewer, so ten open dashboards cost the same as one.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait bounds a single frame write, so one stalled client cannot
	// hold the broadcast loop.
	writeWait = 10 * time.Second
	// pongWait is how long a client may go silent before it is dropped,
	// and pingInterval must be comfortably shorter than it.
	pongWait     = 60 * time.Second
	pingInterval = 25 * time.Second
	// maxMessageSize caps what a client may send. The protocol is one-way,
	// so anything large is either a bug or an attack.
	maxMessageSize = 1024
)

// SnapshotFunc produces the payload broadcast on each tick.
type SnapshotFunc func() any

// Hub owns the set of connected clients and the broadcast loop.
type Hub struct {
	logger   *slog.Logger
	snapshot SnapshotFunc
	interval time.Duration
	upgrader websocket.Upgrader

	mu      sync.RWMutex
	clients map[*client]struct{}
}

// New builds a hub that broadcasts snapshot() every interval.
func New(logger *slog.Logger, interval time.Duration, snapshot SnapshotFunc) *Hub {
	return &Hub{
		logger:   logger.With("component", "ws"),
		snapshot: snapshot,
		interval: interval,
		clients:  make(map[*client]struct{}),
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   1024,
			WriteBufferSize:  16 * 1024,
			// The dashboard is served from this same origin, and the
			// endpoint is behind a bearer token, so the default
			// same-origin check is the right policy: it is what stops
			// another site from opening a socket using the browser's
			// credentials.
		},
	}
}

// Run drives the broadcast loop until ctx is cancelled, then closes every
// client so browsers see a clean shutdown rather than a dropped connection.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	defer h.closeAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.broadcast()
		}
	}
}

// ServeHTTP upgrades a request into a subscription. Authentication happens in
// the middleware in front of it.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own error response.
		h.logger.Debug("websocket upgrade failed", "error", err, "remote", r.RemoteAddr)
		return
	}

	c := &client{
		conn: conn,
		// A small buffer absorbs a slow render without dropping the
		// client; anything beyond it means the client cannot keep up.
		send: make(chan []byte, 8),
	}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	count := len(h.clients)
	h.mu.Unlock()
	h.logger.Debug("dashboard connected", "clients", count)

	// One goroutine writes, one reads. The reader exists to notice a
	// disconnect and to keep the pong deadline fresh, not to accept
	// commands.
	go h.writeLoop(c)
	go h.readLoop(c)

	// The new client gets the current state immediately instead of waiting
	// out a tick with an empty dashboard.
	if payload, err := json.Marshal(h.snapshot()); err == nil {
		c.trySend(payload)
	}
}

// Clients reports how many dashboards are connected.
func (h *Hub) Clients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) broadcast() {
	h.mu.RLock()
	empty := len(h.clients) == 0
	h.mu.RUnlock()
	if empty {
		// Nobody is watching, so the snapshot is not even computed. This
		// matters: taking a snapshot resets the live rate window.
		return
	}

	payload, err := json.Marshal(h.snapshot())
	if err != nil {
		h.logger.Error("encode snapshot", "error", err)
		return
	}

	h.mu.RLock()
	targets := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		if !c.trySend(payload) {
			// A client whose buffer is full is too slow to follow a live
			// feed. Dropping it is better than growing an unbounded queue
			// or stalling every other dashboard.
			h.logger.Debug("dropping a dashboard that cannot keep up")
			h.remove(c)
		}
	}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	_, present := h.clients[c]
	delete(h.clients, c)
	h.mu.Unlock()

	if present {
		c.close()
	}
}

func (h *Hub) closeAll() {
	h.mu.Lock()
	clients := h.clients
	h.clients = make(map[*client]struct{})
	h.mu.Unlock()

	for c := range clients {
		c.close()
	}
}

func (h *Hub) writeLoop(c *client) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	defer h.remove(c)

	for {
		select {
		case payload, ok := <-c.send:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Hub) readLoop(c *client) {
	defer h.remove(c)

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
		// Incoming frames are ignored, but reading them is what keeps the
		// deadline refreshed and detects a closed connection.
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	}
}

// client is one connected dashboard.
type client struct {
	conn *websocket.Conn
	send chan []byte

	// mu guards closed, which both loops may reach at once: either can
	// notice the disconnect first. Closing send twice, or sending on it
	// after it is closed, would panic.
	mu     sync.Mutex
	closed bool
}

// trySend queues a payload without blocking. It reports false when the client
// has gone away or its buffer is full.
func (c *client) trySend(payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (c *client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
	_ = c.conn.Close()
}
