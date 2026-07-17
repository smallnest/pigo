// Package jsonrpc implements a minimal JSON-RPC 2.0 client over a subprocess's
// stdio (US-014/#116). It is the shared transport foundation reused by the MCP
// client (#130/#131), the plugin system (#132/#133) and process-isolated
// sub-agents (#135): each spawns an external executable and speaks line-delimited
// JSON-RPC 2.0 over the child's stdin/stdout.
//
// The wire format follows the spec: every message carries "jsonrpc":"2.0". A
// request has an id and expects a matching response; a notification omits the id
// and expects none. Requests and responses are correlated by id, so concurrent
// requests from different goroutines are safe — each waits only on its own reply.
//
// This file defines the message envelope and (de)serialization; transport.go
// implements the subprocess client.
package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// Version is the only JSON-RPC protocol version this package speaks.
const Version = "2.0"

// ID is a JSON-RPC request identifier. The spec allows a string or a number;
// this client only ever generates numeric ids, but ID round-trips whatever a
// peer sends back so response correlation still works against servers that echo
// string ids.
type ID struct {
	num   int64
	str   string
	isStr bool
}

// NumID returns a numeric request id.
func NumID(n int64) ID { return ID{num: n} }

// String renders the id for use as a map key when correlating responses.
func (id ID) String() string {
	if id.isStr {
		return "s:" + id.str
	}
	return fmt.Sprintf("n:%d", id.num)
}

// MarshalJSON emits the id as its underlying JSON scalar (number or string).
func (id ID) MarshalJSON() ([]byte, error) {
	if id.isStr {
		return json.Marshal(id.str)
	}
	return json.Marshal(id.num)
}

// UnmarshalJSON accepts either a JSON number or string id.
func (id *ID) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		id.num, id.isStr, id.str = n, false, ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		id.str, id.isStr, id.num = s, true, 0
		return nil
	}
	return fmt.Errorf("jsonrpc: id is neither number nor string: %s", data)
}

// Request is an outgoing JSON-RPC request or notification. When ID is nil the
// message is a notification (no response expected).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is an incoming JSON-RPC response. Exactly one of Result / Error is
// set on a well-formed reply.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *ID             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface so a peer error can be returned directly.
func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc: server error %d: %s", e.Code, e.Message)
}

// newRequest builds a request (id != nil) or notification (id == nil) with the
// given params marshaled to JSON. A nil params value is omitted from the wire
// message.
func newRequest(id *ID, method string, params any) (*Request, error) {
	req := &Request{JSONRPC: Version, ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("jsonrpc: marshal params for %q: %w", method, err)
		}
		req.Params = raw
	}
	return req, nil
}
