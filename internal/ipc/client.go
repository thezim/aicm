package ipc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var fallbackID atomic.Uint64

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func nextFallbackID() uint64 { return fallbackID.Add(1) }

// Client is a JSON-lines RPC client with an event stream.
type Client struct {
	conn net.Conn
	enc  *json.Encoder

	mu      sync.Mutex
	pending map[string]chan Response

	events chan Event
	done   chan struct{}
	err    error
	once   sync.Once
}

// Dial connects to the local daemon endpoint.
func Dial(ctx context.Context) (*Client, error) {
	timeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < timeout {
			timeout = d
		}
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	conn, err := dialEndpoint(timeout)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: make(map[string]chan Response),
		events:  make(chan Event, 128),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Call invokes a daemon method. If result is non-nil, the response is decoded
// into it.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	id, err := randomHex(8)
	if err != nil {
		id = fmt.Sprintf("%d", nextFallbackID())
	}
	req := Request{ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = raw
	}
	ch := make(chan Response, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("daemon disconnected")
		}
		return err
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	c.mu.Lock()
	werr := c.enc.Encode(req)
	c.mu.Unlock()
	if werr != nil {
		return werr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.err != nil {
			return c.err
		}
		return errors.New("daemon disconnected")
	case resp := <-ch:
		if !resp.OK {
			if resp.Error != nil {
				return resp.Error
			}
			return errors.New("request failed")
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// Events returns the daemon event stream. The channel closes when the
// connection is lost.
func (c *Client) Events() <-chan Event { return c.events }

// Err returns the terminal connection error, if any.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) readLoop() {
	defer close(c.events)
	defer close(c.done)
	reader := bufio.NewReaderSize(c.conn, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			c.setErr(err)
			c.failPending(err)
			_ = c.conn.Close()
			return
		}
		var env struct {
			ID    string `json:"id"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Event != "" {
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				continue
			}
			select {
			case c.events <- ev:
			default:
				// Slow consumer: drop intermediate events rather than deadlock
				// the daemon connection. State can be refreshed explicitly.
			}
			continue
		}
		if env.ID == "" {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- resp:
			default:
			}
		}
	}
}

func (c *Client) setErr(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan Response)
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- Response{OK: false, Error: &RPCError{Code: "disconnected", Message: err.Error()}}:
		default:
		}
	}
}

// Close closes the client connection.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() {
		err = c.conn.Close()
	})
	return err
}
