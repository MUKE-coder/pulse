package pulse

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// WebSocket message types pushed to clients.
const (
	WSTypeOverview = "overview"
	WSTypeRequest  = "request"
	WSTypeError    = "error"
	WSTypeHealth   = "health"
	WSTypeAlert    = "alert"
	WSTypeRuntime  = "runtime"
)

// WSMessage is the envelope for all WebSocket messages.
type WSMessage struct {
	Type      string      `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

// WSSubscription is sent by clients to subscribe to specific channels.
type WSSubscription struct {
	Subscribe []string `json:"subscribe"`
}

// Client represents a single WebSocket connection.
type Client struct {
	hub  *WebSocketHub
	conn *websocket.Conn

	// Buffered channel for outgoing messages.
	send chan []byte

	// Subscribed channels.
	mu      sync.RWMutex
	channels map[string]bool
}

// WebSocketHub manages all connected WebSocket clients.
type WebSocketHub struct {
	pulse *Pulse

	mu      sync.RWMutex
	clients map[*Client]bool

	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer (subscriptions only, so small).
	maxMessageSize = 512

	// Client send buffer size.
	sendBufferSize = 256
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins — dashboard served from same host
	},
}

// newWebSocketHub creates a new hub but does NOT start it. Call run() separately.
func newWebSocketHub(p *Pulse) *WebSocketHub {
	return &WebSocketHub{
		pulse:      p,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// run is the main event loop for the hub, handling registration, unregistration, and broadcasting.
func (h *WebSocketHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()

			if h.pulse.config.DevMode {
				h.pulse.logger.Printf("[pulse] ws client connected (total: %d)", h.ClientCount())
			}

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

			if h.pulse.config.DevMode {
				h.pulse.logger.Printf("[pulse] ws client disconnected (total: %d)", h.ClientCount())
			}

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// Client buffer full — drop the client
					go func(c *Client) {
						h.unregister <- c
					}(client)
				}
			}
			h.mu.RUnlock()

		case <-h.pulse.ctx.Done():
			// Shutdown: close all clients
			h.mu.Lock()
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			return
		}
	}
}

// ClientCount returns the number of connected clients.
func (h *WebSocketHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast sends a message to all clients subscribed to the given channel.
func (h *WebSocketHub) Broadcast(msgType string, payload interface{}) {
	if h == nil {
		return
	}

	msg := WSMessage{
		Type:      msgType,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		if h.pulse.config.DevMode {
			h.pulse.logger.Printf("[pulse] ws marshal error: %v", err)
		}
		return
	}

	// Only send to subscribed clients
	h.mu.RLock()
	for client := range h.clients {
		if client.isSubscribed(msgType) {
			select {
			case client.send <- data:
			default:
				// Buffer full — schedule disconnect
				go func(c *Client) {
					h.unregister <- c
				}(client)
			}
		}
	}
	h.mu.RUnlock()
}

// BroadcastRaw sends pre-marshaled bytes to all clients subscribed to the given channel.
func (h *WebSocketHub) BroadcastRaw(msgType string, data []byte) {
	if h == nil {
		return
	}

	h.mu.RLock()
	for client := range h.clients {
		if client.isSubscribed(msgType) {
			select {
			case client.send <- data:
			default:
				go func(c *Client) {
					h.unregister <- c
				}(client)
			}
		}
	}
	h.mu.RUnlock()
}

// --- Client methods ---

func (c *Client) isSubscribed(channel string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// If no subscriptions set, subscribe to everything (default)
	if len(c.channels) == 0 {
		return true
	}
	return c.channels[channel]
}

// readPump reads messages from the WebSocket connection.
// It handles subscription messages and ping/pong.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				if c.hub.pulse.config.DevMode {
					log.Printf("[pulse] ws read error: %v", err)
				}
			}
			break
		}

		// Parse subscription message
		var sub WSSubscription
		if err := json.Unmarshal(message, &sub); err == nil && len(sub.Subscribe) > 0 {
			c.mu.Lock()
			c.channels = make(map[string]bool, len(sub.Subscribe))
			for _, ch := range sub.Subscribe {
				c.channels[ch] = true
			}
			c.mu.Unlock()

			if c.hub.pulse.config.DevMode {
				log.Printf("[pulse] ws client subscribed to: %v", sub.Subscribe)
			}
		}
	}
}

// writePump writes messages from the send channel to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Drain any queued messages into the same write
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte("\n"))
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// --- WebSocket Route Registration ---

// registerWebSocketRoute registers the WS endpoint on the router.
func registerWebSocketRoute(router *gin.Engine, p *Pulse) {
	prefix := p.config.Prefix

	router.GET(prefix+"/ws/live", func(c *gin.Context) {
		if p.wsHub == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "websocket hub not available"})
			return
		}

		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			if p.config.DevMode {
				p.logger.Printf("[pulse] ws upgrade failed: %v", err)
			}
			return
		}

		client := &Client{
			hub:      p.wsHub,
			conn:     conn,
			send:     make(chan []byte, sendBufferSize),
			channels: make(map[string]bool),
		}

		p.wsHub.register <- client

		// Start read/write goroutines
		go client.writePump()
		go client.readPump()
	})
}

// --- Broadcast Helpers (called from subsystems) ---

// BroadcastRequest sends a lightweight request event to WS clients.
func (p *Pulse) BroadcastRequest(m RequestMetric) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}

	p.wsHub.Broadcast(WSTypeRequest, map[string]interface{}{
		"method":     m.Method,
		"path":       m.Path,
		"status":     m.StatusCode,
		"latency_ms": float64(m.Latency) / float64(time.Millisecond),
		"trace_id":   m.TraceID,
	})
}

// BroadcastError sends a lightweight error event to WS clients.
func (p *Pulse) BroadcastError(e ErrorRecord) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}

	p.wsHub.Broadcast(WSTypeError, map[string]interface{}{
		"route":   e.Method + " " + e.Route,
		"message": e.ErrorMessage,
		"type":    e.ErrorType,
		"count":   e.Count,
	})
}

// BroadcastHealthResult sends a health check result to WS clients.
func (p *Pulse) BroadcastHealthResult(r HealthCheckResult) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}

	p.wsHub.Broadcast(WSTypeHealth, map[string]interface{}{
		"name":       r.Name,
		"status":     r.Status,
		"latency_ms": float64(r.Latency) / float64(time.Millisecond),
		"error":      r.Error,
	})
}

// BroadcastAlert sends an alert event to WS clients.
func (p *Pulse) BroadcastAlert(a AlertRecord) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}

	p.wsHub.Broadcast(WSTypeAlert, map[string]interface{}{
		"id":       a.ID,
		"rule":     a.RuleName,
		"severity": a.Severity,
		"message":  a.Message,
		"state":    a.State,
	})
}

// BroadcastOverview sends the current overview snapshot to WS clients.
func (p *Pulse) BroadcastOverview(overview *Overview) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 || overview == nil {
		return
	}

	p.wsHub.Broadcast(WSTypeOverview, overview)
}

// BroadcastRuntime sends runtime metrics to WS clients.
func (p *Pulse) BroadcastRuntime(m RuntimeMetric) {
	if p.wsHub == nil || p.wsHub.ClientCount() == 0 {
		return
	}

	p.wsHub.Broadcast(WSTypeRuntime, map[string]interface{}{
		"heap_alloc_mb":  float64(m.HeapAlloc) / 1024 / 1024,
		"heap_in_use_mb": float64(m.HeapInUse) / 1024 / 1024,
		"goroutines":     m.NumGoroutine,
		"gc_pause_us":    m.GCPauseNs / 1000,
		"num_gc":         m.NumGC,
	})
}
