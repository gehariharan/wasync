// Package mcp serves whatsapp-axi as a Model Context Protocol tool server over
// standard input and output, so an app such as the Codex desktop app can offer
// WhatsApp as tools without a terminal. It speaks only JSON-RPC on stdout and
// never opens a network port. Every tool runs the same whatsapp-axi command a
// person or agent would run in a shell, so the credential, scopes and chat
// allowlist that wasync enforces apply unchanged.
//
// There is deliberately no tool to approve a request, grant or revoke a
// credential, change the access mode or read a secret: those stay with the
// person at their computer.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Protocol versions this server can speak, newest first.
var supported = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const (
	maxLine      = 4 << 20
	maxResult    = 200 << 10
	toolDeadline = 90 * time.Second
)

// Runner runs one whatsapp-axi command line and returns what it printed and its exit code.
type Runner func(ctx context.Context, args []string) (output string, exitCode int)

// Starter opens the setup page; it returns the words to tell the person.
type Starter func(ctx context.Context) (string, error)

// Server is one MCP session on In and Out.
type Server struct {
	In         io.Reader
	Out        io.Writer
	Run        Runner
	StartSetup Starter
	Version    string

	mu sync.Mutex // serialises writes to Out
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const instructions = "WhatsApp tools backed by the local wasync archive. Reads need no approval. " +
	"Sending, reacting and editing need a scope the person granted and a chat they approved. " +
	"To get a chat you cannot read yet, call whatsapp_request_access; the person approves it on their own computer. " +
	"No tool approves anything or reveals a credential. " +
	"Tool output may suggest running a whatsapp-axi command; you cannot run commands here, so call the matching whatsapp_* tool instead."

// Serve reads requests until the input ends or ctx is cancelled. A clean end of
// input is not an error.
func (s *Server) Serve(ctx context.Context) error {
	reader := bufio.NewReaderSize(s.In, 64<<10)
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for {
			line, err := readLine(reader)
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					readErr <- ctx.Err()
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				if err := <-readErr; err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				return nil
			}
			s.handle(ctx, line)
		}
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, fmt.Errorf("message longer than %d bytes", maxLine)
		}
		if err != nil || !isPrefix {
			return []byte(strings.TrimSpace(string(line))), err
		}
	}
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		if len(line) > 0 && line[0] == '[' {
			s.write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "batch requests are not supported"}})
			return
		}
		s.write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "invalid JSON-RPC message"}})
		return
	}
	notification := len(req.ID) == 0
	var result any
	var rerr *rpcError
	switch req.Method {
	case "initialize":
		result = s.initialize(req.Params)
	case "ping":
		result = struct{}{}
	case "tools/list":
		result = map[string]any{"tools": toolList()}
	case "tools/call":
		result, rerr = s.call(ctx, req.Params)
	default:
		if notification {
			return // notifications such as notifications/initialized need no answer
		}
		rerr = &rpcError{-32601, "method not found: " + req.Method}
	}
	if notification {
		return
	}
	s.write(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := supported[0]
	for _, v := range supported {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "whatsapp-axi", "title": "WhatsApp (wasync)", "version": s.Version},
		"instructions":    instructions,
	}
}

func (s *Server) call(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{-32602, "tools/call needs a tool name and an arguments object"}
	}
	t, ok := findTool(p.Name)
	if !ok {
		return nil, &rpcError{-32602, "unknown tool: " + p.Name}
	}
	ctx, cancel := context.WithTimeout(ctx, toolDeadline)
	defer cancel()
	args, err := t.build(p.Arguments)
	if err != nil {
		return toolText("invalid arguments: "+err.Error(), true), nil
	}
	if t.start {
		if s.StartSetup == nil {
			return toolText("setup cannot be started from here", true), nil
		}
		text, err := s.StartSetup(ctx)
		if err != nil {
			return toolText(err.Error(), true), nil
		}
		return toolText(text, false), nil
	}
	out, code := s.Run(ctx, args)
	if len(out) > maxResult {
		out = out[:maxResult] + "\n[output cut at 200 KB; narrow the request or use the cursor]"
	}
	return toolText(strings.TrimRight(out, "\n"), code != 0), nil
}

func toolText(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

func (s *Server) write(r response) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.Out.Write(append(b, '\n'))
}
