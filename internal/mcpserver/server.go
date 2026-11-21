// Package mcpserver lets AI assistants work on modpacks through the Model
// Context Protocol (MCP). It is a stdio server: the client (Claude Desktop,
// Claude Code, …) starts "modpackmanager mcp" and exchanges newline-delimited
// JSON-RPC messages with it over stdin and stdout.
//
// The server is "dual-era": it answers the stateless 2026-07-28 protocol,
// where every request carries its version in _meta and clients may probe
// with server/discover, as well as the older revisions (2024-11-05 through
// 2025-11-25) that open with an initialize handshake.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"

	"modpackmanager/internal/core"
	"modpackmanager/internal/version"
)

// modernVersion is the stateless protocol revision.
const modernVersion = "2026-07-28"

// legacyVersions are the initialize-based revisions, newest first.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func supportedVersions() []string {
	return append([]string{modernVersion}, legacyVersions...)
}

// JSON-RPC and MCP error codes.
const (
	codeParseError                 = -32700
	codeInvalidRequest             = -32600
	codeMethodNotFound             = -32601
	codeInvalidParams              = -32602
	codeUnsupportedProtocolVersion = -32022
)

// Reserved _meta keys of the 2026-07-28 revision.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
	metaSubscriptionID     = "io.modelcontextprotocol/subscriptionId"
)

// maxMessage caps one incoming message (one line).
const maxMessage = 32 << 20

// cacheTTL tells modern clients how long tools/list and server/discover
// results stay fresh: the tool set never changes while the process runs.
const cacheTTL = 60 * 60 * 1000 // one hour, in milliseconds

// Options configure a Server.
type Options struct {
	// ReadOnly leaves out the tools that change files.
	ReadOnly bool
	// Log receives diagnostics; with stdio that is stderr. nil discards them.
	Log io.Writer
}

// Server answers MCP requests for one client connection.
type Server struct {
	svc    *core.Service
	opts   Options
	tools  []*tool
	byName map[string]*tool

	// writeMu serialises the tools that change pack files, so two calls on
	// the same pack can't overwrite each other's changes.
	writeMu sync.Mutex

	outMu sync.Mutex
	out   io.Writer

	mu       sync.Mutex
	legacy   string           // version negotiated by initialize, "" if none
	inflight map[string]*call // running tools/call requests by id

	maxLine int // largest accepted message, in bytes
}

type call struct {
	cancel    context.CancelFunc
	cancelled bool
}

// New returns a server that works with packs through svc.
func New(svc *core.Service, opts Options) *Server {
	s := &Server{svc: svc, opts: opts, byName: map[string]*tool{}, inflight: map[string]*call{}, maxLine: maxMessage}
	for _, t := range allTools() {
		if opts.ReadOnly && !t.readOnly {
			continue
		}
		s.tools = append(s.tools, t)
		s.byName[t.name] = t
	}
	return s
}

// Serve reads messages from in and writes replies to out; a Server serves
// one connection, so call it once. It returns nil when
// in is closed (after letting running tool calls finish), or ctx's error when
// ctx ends first (running tool calls are cancelled).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.out = out
	var wg sync.WaitGroup

	type line struct {
		data    []byte
		tooLong bool
	}
	lines := make(chan line)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(in, 64<<10)
		for {
			data, tooLong, err := readLine(r, s.maxLine)
			if tooLong || len(bytes.TrimSpace(data)) > 0 {
				select {
				case lines <- line{data, tooLong}:
				case <-ctx.Done():
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
			wg.Wait()
			return ctx.Err()
		case l := <-lines:
			if l.tooLong {
				s.send(errorReply(nil, codeParseError, fmt.Sprintf("message too large (the limit is %d bytes)", s.maxLine), nil))
				continue
			}
			s.handleLine(ctx, l.data, &wg)
		case err := <-readErr:
			wg.Wait()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// readLine reads one newline-terminated message, reporting lines over max
// bytes instead of buffering them.
func readLine(r *bufio.Reader, max int) (data []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(data)+len(chunk) > max {
				data, tooLong = nil, true
			} else {
				data = append(data, chunk...)
			}
		}
		if err != bufio.ErrBufferFull {
			return data, tooLong, err
		}
	}
}

// ---------------------------------------------------------------------------
// Messages

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func resultReply(id json.RawMessage, result any) *reply {
	return &reply{JSONRPC: "2.0", ID: id, Result: result}
}

func errorReply(id json.RawMessage, code int, msg string, data any) *reply {
	return &reply{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
}

func (s *Server) send(v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // one line, ending in '\n'
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.logf("encoding a reply: %v", err)
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if _, err := s.out.Write(buf.Bytes()); err != nil {
		s.logf("writing a reply: %v", err)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Log != nil {
		fmt.Fprintf(s.opts.Log, "modpackmanager mcp: "+format+"\n", args...)
	}
}

func (s *Server) handleLine(ctx context.Context, data []byte, wg *sync.WaitGroup) {
	data = bytes.TrimSpace(data)
	if data[0] == '[' {
		// A batch: allowed by revision 2025-03-26 only. Its calls run in
		// order and the replies go back together.
		var batch []json.RawMessage
		if err := json.Unmarshal(data, &batch); err != nil {
			s.send(errorReply(nil, codeParseError, "parse error: "+err.Error(), nil))
			return
		}
		if len(batch) == 0 {
			s.send(errorReply(nil, codeInvalidRequest, "empty batch", nil))
			return
		}
		var replies []*reply
		for _, raw := range batch {
			if r := s.handleMessage(ctx, raw, nil); r != nil {
				replies = append(replies, r)
			}
		}
		if len(replies) > 0 {
			s.send(replies)
		}
		return
	}
	if r := s.handleMessage(ctx, data, wg); r != nil {
		s.send(r)
	}
}

// handleMessage answers one message. With wg set, tool calls run in the
// background (their reply is sent when they finish) and nil is returned.
func (s *Server) handleMessage(ctx context.Context, raw []byte, wg *sync.WaitGroup) *reply {
	var msg message
	if !json.Valid(raw) {
		return errorReply(nil, codeParseError, "parse error: not valid JSON", nil)
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return errorReply(nil, codeInvalidRequest, "a message must be a JSON-RPC object", nil)
	}
	hasID := len(msg.ID) > 0
	if msg.Method == "" {
		if hasID && (len(msg.Result) > 0 || len(msg.Error) > 0) {
			return nil // a reply from the client; this server sends no requests
		}
		return errorReply(validID(msg.ID), codeInvalidRequest, "a request needs a method", nil)
	}
	if msg.JSONRPC != "2.0" {
		if !hasID {
			return nil
		}
		return errorReply(validID(msg.ID), codeInvalidRequest, `jsonrpc must be "2.0"`, nil)
	}
	if !hasID {
		s.notification(msg)
		return nil
	}
	if validID(msg.ID) == nil {
		return errorReply(nil, codeInvalidRequest, "the request id must be a string or a number", nil)
	}
	return s.request(ctx, msg, wg)
}

// validID returns the id if it is a string or a number, else nil.
func validID(id json.RawMessage) json.RawMessage {
	id = bytes.TrimSpace(id)
	if len(id) == 0 {
		return nil
	}
	if c := id[0]; c == '"' || c == '-' || (c >= '0' && c <= '9') {
		return id
	}
	return nil
}

func (s *Server) notification(msg message) {
	switch msg.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || len(p.RequestID) == 0 {
			return
		}
		s.mu.Lock()
		if c := s.inflight[string(bytes.TrimSpace(p.RequestID))]; c != nil {
			c.cancelled = true
			c.cancel()
		}
		s.mu.Unlock()
	default:
		// notifications/initialized and anything else need no action.
	}
}

// era is how a request is to be answered.
type era struct {
	modern  bool
	version string
}

// at reports whether the request speaks rev or later (dates compare as text).
func (e era) at(rev string) bool { return e.modern || e.version >= rev }

func (s *Server) request(ctx context.Context, msg message, wg *sync.WaitGroup) *reply {
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(msg.Params) > 0 && !bytes.Equal(bytes.TrimSpace(msg.Params), []byte("null")) {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return errorReply(msg.ID, codeInvalidParams, "params must be an object", nil)
		}
	}
	if msg.Method == "initialize" {
		return s.initialize(msg)
	}
	e, rerr := s.eraOf(msg.Method, params.Meta)
	if rerr != nil {
		return &reply{JSONRPC: "2.0", ID: msg.ID, Error: rerr}
	}
	switch msg.Method {
	case "ping":
		if e.modern {
			break // removed in 2026-07-28
		}
		return resultReply(msg.ID, map[string]any{})
	case "server/discover":
		return resultReply(msg.ID, s.complete(e, map[string]any{
			"supportedVersions": supportedVersions(),
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      instructions,
			"ttlMs":             cacheTTL,
			"cacheScope":        "public",
		}))
	case "subscriptions/listen":
		if !e.modern {
			break
		}
		// Nothing this server offers ever changes, so it can't honour any
		// notification type. Like the reference SDK's server, acknowledge an
		// empty filter and end the subscription straight away.
		sub := map[string]any{metaSubscriptionID: msg.ID}
		s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/subscriptions/acknowledged",
			"params": map[string]any{"_meta": sub, "notifications": map[string]any{}}})
		return resultReply(msg.ID, s.complete(e, map[string]any{"_meta": map[string]any{metaSubscriptionID: msg.ID}}))
	case "tools/list":
		return s.listTools(msg, e)
	case "tools/call":
		return s.callTool(ctx, msg, e, wg)
	}
	return errorReply(msg.ID, codeMethodNotFound, "method not found: "+msg.Method, nil)
}

// eraOf decides how to answer a request: by the version in its _meta
// (2026-07-28), or by the version an earlier initialize settled on.
func (s *Server) eraOf(method string, meta map[string]json.RawMessage) (era, *rpcError) {
	if raw, ok := meta[metaProtocolVersion]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil || v == "" {
			return era{}, &rpcError{Code: codeInvalidParams, Message: "_meta." + metaProtocolVersion + " must be a protocol version string"}
		}
		if v != modernVersion {
			return era{}, &rpcError{
				Code:    codeUnsupportedProtocolVersion,
				Message: "Unsupported protocol version",
				Data:    map[string]any{"supported": supportedVersions(), "requested": v},
			}
		}
		if caps, ok := meta[metaClientCapabilities]; !ok || !isObject(caps) {
			return era{}, &rpcError{Code: codeInvalidParams, Message: "_meta." + metaClientCapabilities + " is required"}
		}
		return era{modern: true, version: v}, nil
	}
	s.mu.Lock()
	legacy := s.legacy
	s.mu.Unlock()
	if legacy != "" || method == "ping" {
		return era{version: legacy}, nil
	}
	return era{}, &rpcError{
		Code: codeInvalidParams,
		Message: fmt.Sprintf("missing _meta.%s: send it on every request (protocol %s), or start with initialize (protocol %s and older)",
			metaProtocolVersion, modernVersion, legacyVersions[0]),
	}
}

func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

// initialize opens a legacy (pre-2026-07-28) session for this process.
func (s *Server) initialize(msg message) *reply {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	v := legacyVersions[0] // the newest, when the client asks for one we don't know
	for _, lv := range legacyVersions {
		if p.ProtocolVersion == lv {
			v = lv
		}
	}
	s.mu.Lock()
	s.legacy = v
	s.mu.Unlock()
	return resultReply(msg.ID, map[string]any{
		"protocolVersion": v,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      serverInfo(),
		"instructions":    instructions,
	})
}

func serverInfo() map[string]any {
	return map[string]any{"name": "modpackmanager", "title": "Modpack Manager", "version": version.String()}
}

// complete adds the fields every 2026-07-28 result carries.
func (s *Server) complete(e era, res map[string]any) map[string]any {
	if e.modern {
		res["resultType"] = "complete"
		meta, _ := res["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta[metaServerInfo] = serverInfo()
		res["_meta"] = meta
	}
	return res
}

func (s *Server) listTools(msg message, e era) *reply {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	if p.Cursor != "" {
		return errorReply(msg.ID, codeInvalidParams, "invalid cursor (the tool list has a single page)", nil)
	}
	list := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		list = append(list, t.definition(e))
	}
	res := map[string]any{"tools": list}
	if e.modern {
		res["ttlMs"] = cacheTTL
		res["cacheScope"] = "public"
	}
	return resultReply(msg.ID, s.complete(e, res))
}

func (s *Server) callTool(ctx context.Context, msg message, e era, wg *sync.WaitGroup) *reply {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.Name == "" {
		return errorReply(msg.ID, codeInvalidParams, "tools/call needs the name of a tool", nil)
	}
	t := s.byName[p.Name]
	if t == nil {
		hint := ""
		if s.opts.ReadOnly {
			for _, all := range allTools() {
				if all.name == p.Name {
					hint = " (the server runs with --read-only)"
				}
			}
		}
		return errorReply(msg.ID, codeInvalidParams, "Unknown tool: "+p.Name+hint, nil)
	}
	if wg == nil {
		return resultReply(msg.ID, s.runTool(ctx, t, p.Arguments, e))
	}
	key := string(bytes.TrimSpace(msg.ID))
	cctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.inflight[key] != nil {
		s.mu.Unlock()
		cancel()
		return errorReply(msg.ID, codeInvalidRequest, "a request with this id is still running", nil)
	}
	s.inflight[key] = &call{cancel: cancel}
	s.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		res := s.runTool(cctx, t, p.Arguments, e)
		s.mu.Lock()
		c := s.inflight[key]
		delete(s.inflight, key)
		s.mu.Unlock()
		if c != nil && c.cancelled {
			return // the client gave up on it: no reply
		}
		s.send(resultReply(msg.ID, res))
	}()
	return nil
}

// runTool runs a tool and wraps what it returns (or the error) as a
// CallToolResult. Errors become isError results so the model can read them
// and correct itself.
func (s *Server) runTool(ctx context.Context, t *tool, args json.RawMessage, e era) (res map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("%s crashed: %v\n%s", t.name, r, debug.Stack())
			res = s.toolResult(e, nil, fmt.Errorf("internal error in %s: %v", t.name, r))
		}
	}()
	if trimmed := bytes.TrimSpace(args); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		args = json.RawMessage("{}")
	}
	out, err := t.run(ctx, s, args)
	return s.toolResult(e, out, err)
}

func (s *Server) toolResult(e era, out any, err error) map[string]any {
	if err == nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err = enc.Encode(out); err == nil {
			res := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(bytes.TrimSpace(buf.Bytes()))}}}
			if e.at("2025-06-18") {
				res["structuredContent"] = out
			}
			return s.complete(e, res)
		}
	}
	return s.complete(e, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": err.Error()}},
		"isError": true,
	})
}
