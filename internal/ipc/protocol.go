package ipc

import (
	"encoding/json"

	"github.com/thezim/aicm/internal/model"
)

// ProtocolVersion is bumped on incompatible protocol changes.
const ProtocolVersion = 1

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Event struct {
	Event      string            `json:"event"`
	Connection *model.Connection `json:"connection,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
}

func marshalEvent(ev Event) ([]byte, error) {
	return json.Marshal(ev)
}
