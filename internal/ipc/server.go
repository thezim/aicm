package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Handler processes one RPC request. Returning an error produces an error
// response; params are the raw JSON request parameters.
type Handler func(ctx context.Context, client *ServerClient, method string, params json.RawMessage) (any, error)

// ServerClient is the server-side view of a connected client.
type ServerClient struct {
	id   string
	conn net.Conn
	enc  *json.Encoder
	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

// ID returns a per-connection identifier used only in daemon logs.
func (c *ServerClient) ID() string { return c.id }

// Send writes one JSON message to the client.
func (c *ServerClient) Send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(v)
}

func (c *ServerClient) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// Server is a local JSON-lines RPC server.
type Server struct {
	ln      net.Listener
	handler Handler

	mu      sync.Mutex
	clients map[*ServerClient]struct{}
	closed  bool
}

func NewServer(handler Handler) (*Server, error) {
	ln, err := Listen()
	if err != nil {
		return nil, err
	}
	return &Server{ln: ln, handler: handler, clients: make(map[*ServerClient]struct{})}, nil
}

// Serve accepts clients until the context is canceled or Close is called.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		client := &ServerClient{
			id:   newID(),
			conn: conn,
			enc:  json.NewEncoder(conn),
			done: make(chan struct{}),
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			client.close()
			return nil
		}
		s.clients[client] = struct{}{}
		s.mu.Unlock()
		go s.handle(ctx, client)
	}
}

func (s *Server) handle(ctx context.Context, client *ServerClient) {
	defer func() {
		s.mu.Lock()
		delete(s.clients, client)
		s.mu.Unlock()
		client.close()
	}()
	reader := bufio.NewReaderSize(client.conn, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.Method == "" {
			continue
		}
		result, herr := s.handler(ctx, client, req.Method, req.Params)
		resp := Response{ID: req.ID, OK: herr == nil}
		if herr != nil {
			code := "error"
			msg := herr.Error()
			var rpcErr *RPCError
			if errors.As(herr, &rpcErr) {
				code = rpcErr.Code
				msg = rpcErr.Message
			}
			resp.Error = &RPCError{Code: code, Message: msg}
		} else if result != nil {
			raw, err := json.Marshal(result)
			if err != nil {
				resp.OK = false
				resp.Error = &RPCError{Code: "internal", Message: err.Error()}
			} else {
				resp.Result = raw
			}
		}
		if err := client.Send(resp); err != nil {
			return
		}
	}
}

// Broadcast sends an event to every connected client.
func (s *Server) Broadcast(ev Event) {
	raw, err := marshalEvent(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	clients := make([]*ServerClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.Send(json.RawMessage(raw))
	}
}

// ClientCount returns the number of connected clients.
func (s *Server) ClientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// Close stops accepting and disconnects all clients.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	clients := make([]*ServerClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	_ = s.ln.Close()
	for _, c := range clients {
		c.close()
	}
}

// ErrAlreadyRunning is returned by NewServer if another daemon owns the
// endpoint. Defined in the platform endpoint files.

func newID() string {
	b, err := randomHex(8)
	if err != nil {
		return fmt.Sprintf("%d", nextFallbackID())
	}
	return b
}
