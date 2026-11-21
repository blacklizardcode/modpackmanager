package mcpserver

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"modpackmanager/internal/api/curseforge"
	"modpackmanager/internal/api/modrinth"
	"modpackmanager/internal/core"
)

// ---------------------------------------------------------------------------
// A fake Modrinth (plus Fabric meta and a download host)

var cdnJars = map[string][]byte{
	"sodium.jar":    []byte("sodium-bytes"),
	"fabricapi.jar": []byte("fabricapi-bytes"),
	"lithium.jar":   []byte("lithium-bytes"),
}

func h1(b []byte) string   { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
func h512(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

type fakeNet struct {
	srv *httptest.Server

	mu         sync.Mutex
	calls      []string
	facets     []string
	published  map[string][]byte // jars that are "on Modrinth", by version id
	searchHook func(r *http.Request)
}

func (f *fakeNet) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeNet) lastFacets() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.facets) == 0 {
		return ""
	}
	return f.facets[len(f.facets)-1]
}

func newFakeNet(t *testing.T) (*fakeNet, *core.Service) {
	f := &fakeNet{published: map[string][]byte{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	base := f.srv.URL

	ver := func(id, pid, file, env string, deps []map[string]string) map[string]any {
		jar := cdnJars[file]
		return map[string]any{
			"id": id, "project_id": pid, "version_number": "1.0", "version_type": "release",
			"date_published": "2025-01-01T00:00:00Z", "environment": env, "game_versions": []string{"1.21.1"}, "loaders": []string{"fabric"},
			"files":        []map[string]any{{"url": base + "/cdn/" + file, "filename": file, "primary": true, "size": len(jar), "hashes": map[string]string{"sha1": h1(jar), "sha512": h512(jar)}}},
			"dependencies": deps,
		}
	}
	pubVersion := func(id string, jar []byte) map[string]any {
		return map[string]any{"id": id, "project_id": "P-" + id, "version_number": "9.9", "environment": "client_and_server",
			"files": []map[string]any{{"url": "https://cdn.modrinth.com/data/P-" + id + "/versions/" + id + "/" + strings.ToLower(id) + ".jar",
				"filename": strings.ToLower(id) + ".jar", "primary": true, "size": len(jar), "hashes": map[string]string{"sha1": h1(jar), "sha512": h512(jar)}}}}
	}
	versions := map[string]map[string]any{
		"SOD1": ver("SOD1", "AANobbMI", "sodium.jar", "client_only", []map[string]string{{"project_id": "P7dR8mSH", "dependency_type": "required"}}),
		"FAPI": ver("FAPI", "P7dR8mSH", "fabricapi.jar", "client_and_server", nil),
		"LITH": ver("LITH", "gvQqBUqZ", "lithium.jar", "client_and_server", nil),
	}
	projects := map[string]map[string]any{
		"AANobbMI": {"id": "AANobbMI", "slug": "sodium", "title": "Sodium", "description": "Fast renderer", "client_side": "required", "server_side": "unsupported", "project_type": "mod", "team": "T1"},
		"P7dR8mSH": {"id": "P7dR8mSH", "slug": "fabric-api", "title": "Fabric API", "description": "Core library", "client_side": "required", "server_side": "required", "project_type": "mod", "team": "T2"},
		"gvQqBUqZ": {"id": "gvQqBUqZ", "slug": "lithium", "title": "Lithium", "description": "Server optimisation", "client_side": "optional", "server_side": "required", "project_type": "mod", "team": "T3"},
	}
	bySlug := map[string]string{"sodium": "AANobbMI", "fabric-api": "P7dR8mSH", "lithium": "gvQqBUqZ"}
	projectVersion := map[string]string{"AANobbMI": "SOD1", "P7dR8mSH": "FAPI", "gvQqBUqZ": "LITH"}

	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	ids := func(r *http.Request) []string {
		var out []string
		json.Unmarshal([]byte(r.URL.Query().Get("ids")), &out)
		return out
	}
	mux.HandleFunc("/mr/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/mr")
		f.mu.Lock()
		f.calls = append(f.calls, p)
		hook := f.searchHook
		if p == "/search" {
			f.facets = append(f.facets, r.URL.Query().Get("facets"))
		}
		f.mu.Unlock()
		switch {
		case p == "/search":
			if hook != nil {
				hook(r)
			}
			js(w, map[string]any{"total_hits": 2, "hits": []map[string]any{
				{"project_id": "AANobbMI", "slug": "sodium", "title": "Sodium", "description": "Fast renderer", "downloads": 100, "client_side": "required", "server_side": "unsupported", "author": "jelly", "project_type": "mod"},
				{"project_id": "gvQqBUqZ", "slug": "lithium", "title": "Lithium", "description": "Server optimisation", "downloads": 10, "client_side": "optional", "server_side": "required", "author": "jelly", "project_type": "mod"},
			}})
		case p == "/version_files":
			var body struct{ Hashes []string }
			json.NewDecoder(r.Body).Decode(&body)
			out := map[string]any{}
			f.mu.Lock()
			for _, h := range body.Hashes {
				for _, v := range versions {
					if v["files"].([]map[string]any)[0]["hashes"].(map[string]string)["sha1"] == h {
						out[h] = v
					}
				}
				for id, jar := range f.published {
					if h1(jar) == h {
						out[h] = pubVersion(id, jar)
					}
				}
			}
			f.mu.Unlock()
			js(w, out)
		case p == "/projects":
			var out []any
			for _, id := range ids(r) {
				if pr, ok := projects[id]; ok {
					out = append(out, pr)
				} else if strings.HasPrefix(id, "P-") {
					out = append(out, map[string]any{"id": id, "slug": strings.ToLower(id), "title": "Published " + strings.TrimPrefix(id, "P-"), "description": "From Modrinth",
						"project_type": "mod", "team": "TP", "client_side": "required", "server_side": "required"})
				}
			}
			js(w, out)
		case p == "/versions":
			var out []any
			f.mu.Lock()
			for _, id := range ids(r) {
				if v, ok := versions[id]; ok {
					out = append(out, v)
				} else if jar, ok := f.published[id]; ok {
					out = append(out, pubVersion(id, jar))
				}
			}
			f.mu.Unlock()
			js(w, out)
		case p == "/teams":
			var out [][]map[string]any
			for _, id := range ids(r) {
				out = append(out, []map[string]any{{"team_id": id, "role": "Owner", "user": map[string]string{"username": "owner-" + id}}})
			}
			js(w, out)
		case strings.HasPrefix(p, "/project/") && strings.HasSuffix(p, "/version"):
			id := strings.TrimSuffix(strings.TrimPrefix(p, "/project/"), "/version")
			if vid, ok := projectVersion[id]; ok {
				js(w, []any{versions[vid]})
			} else {
				js(w, []any{})
			}
		case strings.HasPrefix(p, "/project/") && strings.HasSuffix(p, "/members"):
			js(w, []map[string]any{{"role": "Owner", "user": map[string]string{"username": "jelly"}}})
		case strings.HasPrefix(p, "/project/"):
			id := strings.TrimPrefix(p, "/project/")
			if real, ok := bySlug[strings.ToLower(id)]; ok {
				id = real
			}
			if pr, ok := projects[id]; ok {
				js(w, pr)
			} else {
				http.NotFound(w, r)
			}
		case strings.HasPrefix(p, "/version/"):
			if v, ok := versions[strings.TrimPrefix(p, "/version/")]; ok {
				js(w, v)
			} else {
				http.NotFound(w, r)
			}
		default:
			t.Errorf("unexpected Modrinth call %s", p)
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/fabric", func(w http.ResponseWriter, r *http.Request) {
		js(w, []map[string]any{{"version": "0.17.2", "stable": true}, {"version": "0.17.3-beta.1", "stable": false}, {"version": "0.16.9", "stable": true}})
	})
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, r *http.Request) {
		b, ok := cdnJars[strings.TrimPrefix(r.URL.Path, "/cdn/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})

	old := core.FabricMetaURL
	core.FabricMetaURL = base + "/fabric"
	t.Cleanup(func() { core.FabricMetaURL = old })
	return f, &core.Service{MR: &modrinth.Client{BaseURL: base + "/mr", HTTP: f.srv.Client()}, CF: &curseforge.Client{}}
}

// testDirs keeps config, cache and home inside the test's temp folder and
// returns a folder for packs.
func testDirs(t *testing.T) string {
	root := t.TempDir()
	// HOME is the home folder on Unix, USERPROFILE on Windows.
	for env, sub := range map[string]string{"MPM_CONFIG_DIR": "config", "MPM_CACHE_DIR": "cache", "HOME": "home", "USERPROFILE": "home"} {
		d := filepath.Join(root, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, d)
	}
	packs := filepath.Join(root, "packs")
	if err := os.MkdirAll(packs, 0o755); err != nil {
		t.Fatal(err)
	}
	return packs
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(files[name])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fabricJar(t *testing.T, id, version, env string) []byte {
	return zipOf(t, map[string][]byte{"fabric.mod.json": []byte(`{"schemaVersion":1,"id":"` + id + `","name":"` + strings.ToUpper(id[:1]) + id[1:] +
		`","version":"` + version + `","environment":"` + env + `","description":"About ` + id + `"}`)})
}

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func zipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

// ---------------------------------------------------------------------------
// A client that talks to Serve the way an MCP client does: one JSON message
// per line on the server's stdin, replies read from its stdout.

type reply_ struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	} `json:"error"`
}

type client struct {
	t       *testing.T
	w       *io.PipeWriter
	lines   chan []byte
	served  chan error
	once    sync.Once
	nextID  int
	early   map[string]reply_
	version string // "" speaks 2026-07-28 (with _meta); otherwise the version initialize settled on
}

func newClient(t *testing.T, s *Server) *client {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, w: inW, lines: make(chan []byte, 1024), served: make(chan error, 1), early: map[string]reply_{}, nextID: 1}
	go func() {
		err := s.Serve(context.Background(), inR, outW)
		outW.Close()
		c.served <- err
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				c.lines <- line
			}
			if err != nil {
				close(c.lines)
				return
			}
		}
	}()
	t.Cleanup(func() {
		if err := c.close(); err != nil {
			t.Errorf("Serve returned %v", err)
		}
	})
	return c
}

// close ends the server's stdin and returns what Serve returned.
func (c *client) close() error {
	c.once.Do(func() { c.w.Close() })
	select {
	case err := <-c.served:
		c.served <- err
		return err
	case <-time.After(20 * time.Second):
		return fmt.Errorf("Serve didn't return after stdin was closed")
	}
}

// rest closes stdin and returns every line the server still wrote.
func (c *client) rest() [][]byte {
	c.t.Helper()
	if err := c.close(); err != nil {
		c.t.Fatal(err)
	}
	var out [][]byte
	for l := range c.lines {
		out = append(out, l)
	}
	return out
}

func (c *client) line(s string) {
	c.t.Helper()
	if strings.Contains(s, "\n") {
		c.t.Fatal("a message must be one line")
	}
	if _, err := io.WriteString(c.w, s+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.line(string(b))
}

func (c *client) readLine() []byte {
	c.t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			c.t.Fatal("the server closed stdout")
		}
		return l
	case <-time.After(30 * time.Second):
		c.t.Fatal("no reply from the server")
	}
	return nil
}

func (c *client) readReply() reply_ {
	c.t.Helper()
	var r reply_
	l := c.readLine()
	if err := json.Unmarshal(l, &r); err != nil {
		c.t.Fatalf("reply %s: %v", l, err)
	}
	return r
}

func modernMeta() map[string]any {
	return map[string]any{
		metaProtocolVersion:                  modernVersion,
		metaClientCapabilities:               map[string]any{},
		"io.modelcontextprotocol/clientInfo": map[string]any{"name": "test", "version": "1"},
	}
}

// start sends a request without waiting; it returns the id.
func (c *client) start(method string, params map[string]any) string {
	c.t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if c.version == "" {
		params["_meta"] = modernMeta()
	}
	id := strconv.Itoa(c.nextID)
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params})
	return id
}

// raw sends params exactly as given (no _meta added) and waits for the reply.
func (c *client) raw(method string, params any) reply_ {
	c.t.Helper()
	id := strconv.Itoa(c.nextID)
	c.nextID++
	msg := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.send(msg)
	return c.wait(id)
}

func (c *client) wait(id string) reply_ {
	c.t.Helper()
	if r, ok := c.early[id]; ok {
		delete(c.early, id)
		return r
	}
	for {
		r := c.readReply()
		if string(r.ID) == id {
			return r
		}
		c.early[string(r.ID)] = r
	}
}

func (c *client) request(method string, params map[string]any) reply_ {
	c.t.Helper()
	return c.wait(c.start(method, params))
}

func (c *client) initialize(version string) reply_ {
	c.t.Helper()
	r := c.raw("initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if r.Error != nil {
		c.t.Fatalf("initialize: %+v", r.Error)
	}
	c.version = r.Result["protocolVersion"].(string)
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return r
}

func textOf(t *testing.T, res map[string]any) string {
	t.Helper()
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content %v", res["content"])
	}
	item := content[0].(map[string]any)
	if item["type"] != "text" {
		t.Fatalf("content type %v", item["type"])
	}
	return item["text"].(string)
}

func (c *client) callTool(name string, args map[string]any) map[string]any {
	c.t.Helper()
	r := c.request("tools/call", map[string]any{"name": name, "arguments": args})
	if r.Error != nil {
		c.t.Fatalf("%s: protocol error %d: %s", name, r.Error.Code, r.Error.Message)
	}
	if c.version == "" && r.Result["resultType"] != "complete" {
		c.t.Fatalf("%s: resultType %v", name, r.Result["resultType"])
	}
	return r.Result
}

// ok calls a tool that must succeed and returns its structured result.
func (c *client) ok(name string, args map[string]any) map[string]any {
	c.t.Helper()
	res := c.callTool(name, args)
	text := textOf(c.t, res)
	if res["isError"] == true {
		c.t.Fatalf("%s failed: %s", name, text)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text), &fromText); err != nil {
		c.t.Fatalf("%s: text isn't JSON: %v", name, err)
	}
	if sc, ok := res["structuredContent"]; ok {
		if !reflect.DeepEqual(sc, any(fromText)) {
			c.t.Fatalf("%s: text and structuredContent differ:\n%s\n%v", name, text, sc)
		}
	} else if c.version == "" || c.version >= "2025-06-18" {
		c.t.Fatalf("%s: no structuredContent", name)
	}
	return fromText
}

// fails calls a tool that must fail with a message containing want.
func (c *client) fails(name string, args map[string]any, want string) {
	c.t.Helper()
	res := c.callTool(name, args)
	text := textOf(c.t, res)
	if res["isError"] != true {
		c.t.Fatalf("%s succeeded, want an error with %q: %s", name, want, text)
	}
	if !strings.Contains(text, want) {
		c.t.Fatalf("%s error %q doesn't mention %q", name, text, want)
	}
}

func list(v any) []any { l, _ := v.([]any); return l }

func names(mods any) []string {
	var out []string
	for _, m := range list(mods) {
		out = append(out, m.(map[string]any)["name"].(string))
	}
	return out
}

func newServer(t *testing.T, opts Options) (*Server, *fakeNet) {
	f, svc := newFakeNet(t)
	return New(svc, opts), f
}

// ---------------------------------------------------------------------------
// Protocol

var toolNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func TestDiscoverAndToolList(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)

	r := c.request("server/discover", nil)
	if r.Error != nil {
		t.Fatal(r.Error.Message)
	}
	res := r.Result
	if !reflect.DeepEqual(res["supportedVersions"], []any{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}) {
		t.Fatalf("supportedVersions %v", res["supportedVersions"])
	}
	if res["resultType"] != "complete" || res["cacheScope"] != "public" || res["ttlMs"] != float64(cacheTTL) {
		t.Fatalf("discover %v", res)
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities %v", res["capabilities"])
	}
	if !strings.Contains(res["instructions"].(string), ".mpm") {
		t.Fatalf("instructions %v", res["instructions"])
	}
	info := res["_meta"].(map[string]any)[metaServerInfo].(map[string]any)
	if info["name"] != "modpackmanager" || info["version"] == "" {
		t.Fatalf("serverInfo %v", info)
	}

	r = c.request("tools/list", nil)
	if r.Error != nil {
		t.Fatal(r.Error.Message)
	}
	if r.Result["resultType"] != "complete" || r.Result["ttlMs"] != float64(cacheTTL) || r.Result["cacheScope"] != "public" {
		t.Fatalf("tools/list %v", r.Result)
	}
	tools := list(r.Result["tools"])
	var got []string
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name := tl["name"].(string)
		got = append(got, name)
		if !toolNameRE.MatchString(name) || tl["title"] == "" || tl["description"] == "" {
			t.Errorf("tool %v", tl)
		}
		schema := tl["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false || props == nil {
			t.Errorf("%s schema %v", name, schema)
		}
		for _, req := range list(schema["required"]) {
			if _, ok := props[req.(string)]; !ok {
				t.Errorf("%s requires %v, which isn't a property", name, req)
			}
		}
		ann := tl["annotations"].(map[string]any)
		ro := slices.Contains([]string{"pack_info", "list_mods", "search_mods"}, name)
		if ann["readOnlyHint"] != ro {
			t.Errorf("%s readOnlyHint %v", name, ann["readOnlyHint"])
		}
		if !ro && ann["destructiveHint"] != (name == "remove_mods") {
			t.Errorf("%s destructiveHint %v", name, ann["destructiveHint"])
		}
		if _, ok := ann["openWorldHint"].(bool); !ok {
			t.Errorf("%s has no openWorldHint", name)
		}
	}
	want := []string{"pack_info", "list_mods", "search_mods", "create_pack", "import_pack", "update_pack_info", "add_mod", "add_jars", "remove_mods", "set_side", "move_to_group", "export_pack"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools %v", got)
	}
	// The list is the same every time.
	r2 := c.request("tools/list", nil)
	if !reflect.DeepEqual(r.Result["tools"], r2.Result["tools"]) {
		t.Fatal("tools/list changed between calls")
	}
}

func TestModernRequestChecks(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	caps := map[string]any{}
	cases := []struct {
		name, method string
		params       any
		code         int
		want         string
	}{
		{"no _meta and no initialize", "tools/list", nil, codeInvalidParams, "initialize"},
		{"unknown version", "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: "2099-01-01", metaClientCapabilities: caps}}, codeUnsupportedProtocolVersion, "Unsupported"},
		{"legacy version in _meta", "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: "2025-11-25", metaClientCapabilities: caps}}, codeUnsupportedProtocolVersion, "Unsupported"},
		{"version not a string", "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: 5, metaClientCapabilities: caps}}, codeInvalidParams, "protocolVersion"},
		{"no client capabilities", "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: modernVersion}}, codeInvalidParams, "clientCapabilities"},
		{"capabilities not an object", "tools/list", map[string]any{"_meta": map[string]any{metaProtocolVersion: modernVersion, metaClientCapabilities: "all"}}, codeInvalidParams, "clientCapabilities"},
		{"params not an object", "tools/list", []any{1, 2}, codeInvalidParams, "object"},
		{"ping is gone", "ping", map[string]any{"_meta": modernMeta()}, codeMethodNotFound, "ping"},
		{"unknown method", "resources/list", map[string]any{"_meta": modernMeta()}, codeMethodNotFound, "resources/list"},
		{"unknown tool", "tools/call", map[string]any{"_meta": modernMeta(), "name": "nope"}, codeInvalidParams, "Unknown tool: nope"},
		{"tool without a name", "tools/call", map[string]any{"_meta": modernMeta()}, codeInvalidParams, "name"},
		{"cursor", "tools/list", map[string]any{"_meta": modernMeta(), "cursor": "abc"}, codeInvalidParams, "cursor"},
	}
	for _, tc := range cases {
		r := c.raw(tc.method, tc.params)
		if r.Error == nil || r.Error.Code != tc.code || !strings.Contains(r.Error.Message, tc.want) {
			t.Errorf("%s: got %+v %v, want code %d with %q", tc.name, r.Error, r.Result, tc.code, tc.want)
		}
		if tc.code == codeUnsupportedProtocolVersion && r.Error != nil {
			if list(r.Error.Data["supported"])[0] != modernVersion || r.Error.Data["requested"] == nil {
				t.Errorf("%s: data %v", tc.name, r.Error.Data)
			}
		}
	}
}

func TestSubscriptionsListen(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	id := c.start("subscriptions/listen", map[string]any{"notifications": map[string]any{"toolsListChanged": true}})
	// First the acknowledgment, with nothing agreed to, then the end of the stream.
	var ack struct {
		Method string
		Params struct {
			Meta          map[string]json.RawMessage `json:"_meta"`
			Notifications map[string]any
		}
	}
	if err := json.Unmarshal(c.readLine(), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Method != "notifications/subscriptions/acknowledged" || string(ack.Params.Meta[metaSubscriptionID]) != id || len(ack.Params.Notifications) != 0 || ack.Params.Notifications == nil {
		t.Fatalf("acknowledgment %+v", ack)
	}
	r := c.readReply()
	meta, _ := r.Result["_meta"].(map[string]any)
	if string(r.ID) != id || r.Error != nil || r.Result["resultType"] != "complete" || fmt.Sprint(meta[metaSubscriptionID]) != id || meta[metaServerInfo] == nil {
		t.Fatalf("listen result %+v", r)
	}
	// Legacy sessions don't have it.
	s2, _ := newServer(t, Options{})
	c2 := newClient(t, s2)
	c2.initialize("2025-11-25")
	if r := c2.request("subscriptions/listen", nil); r.Error == nil || r.Error.Code != codeMethodNotFound {
		t.Fatalf("legacy listen: %+v", r)
	}
}

func TestMalformedMessages(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	cases := []struct {
		line string
		id   string
		code int
	}{
		{`{not json`, "null", codeParseError},
		{`42`, "null", codeInvalidRequest},
		{`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`, "1", codeInvalidRequest},
		{`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`, "null", codeInvalidRequest},
		{`{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`, "null", codeInvalidRequest},
		{`{"jsonrpc":"2.0","id":"x"}`, `"x"`, codeInvalidRequest},
	}
	for _, tc := range cases {
		c.line(tc.line)
		r := c.readReply()
		if r.Error == nil || r.Error.Code != tc.code || string(r.ID) != tc.id {
			t.Errorf("%s: got id %s error %+v", tc.line, r.ID, r.Error)
		}
	}
	// These get no reply: a reply from the client, notifications, blank lines.
	c.line(`{"jsonrpc":"2.0","id":9,"result":{}}`)
	c.line(`{"jsonrpc":"2.0","method":"notifications/whatever","params":{}}`)
	c.line(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":12345}}`)
	c.line(``)
	c.line(`   `)
	id := c.start("tools/list", nil)
	if r := c.readReply(); string(r.ID) != id || r.Error != nil {
		t.Fatalf("expected only the tools/list reply, got id %s %+v", r.ID, r.Error)
	}
}

func TestLegacySessions(t *testing.T) {
	testDirs(t)
	for _, v := range append(slices.Clone(legacyVersions), "2099-01-01") {
		t.Run(v, func(t *testing.T) {
			dir := testDirs(t)
			s, _ := newServer(t, Options{})
			c := newClient(t, s)
			if r := c.raw("ping", nil); r.Error != nil || r.Result == nil {
				t.Fatalf("ping before initialize: %+v", r)
			}
			r := c.initialize(v)
			want := v
			if v == "2099-01-01" {
				want = "2025-11-25"
			}
			res := r.Result
			if res["protocolVersion"] != want || res["serverInfo"].(map[string]any)["name"] != "modpackmanager" || res["instructions"] == "" {
				t.Fatalf("initialize %v", res)
			}
			if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
				t.Fatalf("capabilities %v", res["capabilities"])
			}
			if _, ok := res["resultType"]; ok {
				t.Fatal("legacy result has resultType")
			}
			lr := c.request("tools/list", nil)
			if lr.Error != nil {
				t.Fatal(lr.Error.Message)
			}
			for _, k := range []string{"resultType", "ttlMs", "cacheScope", "_meta"} {
				if _, ok := lr.Result[k]; ok {
					t.Errorf("legacy tools/list has %s", k)
				}
			}
			first := list(lr.Result["tools"])[0].(map[string]any)
			_, hasTitle := first["title"]
			_, hasAnn := first["annotations"]
			if hasTitle != (want >= "2025-06-18") || hasAnn != (want >= "2025-03-26") {
				t.Errorf("tool fields for %s: title %v annotations %v", want, hasTitle, hasAnn)
			}
			res = c.callTool("create_pack", map[string]any{"pack": filepath.Join(dir, "p.mpm"), "name": "P", "minecraft": "1.21.1", "loader": "fabric", "loaderVersion": "0.16.9"})
			if _, ok := res["structuredContent"]; ok != (want >= "2025-06-18") {
				t.Errorf("structuredContent for %s: %v", want, ok)
			}
			if _, ok := res["resultType"]; ok {
				t.Error("legacy tool result has resultType")
			}
			if out := c.ok("pack_info", map[string]any{"pack": filepath.Join(dir, "p.mpm")}); out["name"] != "P" {
				t.Errorf("pack_info %v", out)
			}
			if pr := c.raw("ping", nil); pr.Error != nil {
				t.Errorf("ping: %+v", pr.Error)
			}
		})
	}
}

func TestLegacyBatch(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	c.initialize("2025-03-26")
	c.line(`[{"jsonrpc":"2.0","id":"a","method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":"b","method":"tools/list"}]`)
	var batch []reply_
	if err := json.Unmarshal(c.readLine(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || string(batch[0].ID) != `"a"` || string(batch[1].ID) != `"b"` || len(list(batch[1].Result["tools"])) != 12 {
		t.Fatalf("batch replies %+v", batch)
	}
	c.line(`[]`)
	if r := c.readReply(); r.Error == nil || r.Error.Code != codeInvalidRequest {
		t.Fatalf("empty batch: %+v", r)
	}
	c.line(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)
	id := c.start("ping", nil)
	if r := c.readReply(); string(r.ID) != id {
		t.Fatalf("a batch of notifications got a reply: %s", r.ID)
	}
}

func TestReadOnly(t *testing.T) {
	dir := testDirs(t)
	s, _ := newServer(t, Options{ReadOnly: true})
	c := newClient(t, s)
	var got []string
	for _, raw := range list(c.request("tools/list", nil).Result["tools"]) {
		tl := raw.(map[string]any)
		got = append(got, tl["name"].(string))
		if tl["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Errorf("%v isn't read-only", tl["name"])
		}
	}
	if !reflect.DeepEqual(got, []string{"pack_info", "list_mods", "search_mods"}) {
		t.Fatalf("read-only tools %v", got)
	}
	r := c.request("tools/call", map[string]any{"name": "create_pack", "arguments": map[string]any{"pack": filepath.Join(dir, "x.mpm")}})
	if r.Error == nil || r.Error.Code != codeInvalidParams || !strings.Contains(r.Error.Message, "--read-only") {
		t.Fatalf("create_pack in read-only mode: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.mpm")); err == nil {
		t.Fatal("read-only server wrote a file")
	}
}

func TestCancelledCallGetsNoReply(t *testing.T) {
	testDirs(t)
	s, f := newServer(t, Options{})
	entered, aborted := make(chan struct{}, 1), make(chan struct{})
	f.searchHook = func(r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			close(aborted)
		case <-time.After(20 * time.Second):
		}
	}
	c := newClient(t, s)
	id := c.start("tools/call", map[string]any{"name": "search_mods", "arguments": map[string]any{"query": "slow"}})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the search never started")
	}
	// A second request with the same id while the first runs is refused.
	c.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": "tools/call",
		"params": map[string]any{"_meta": modernMeta(), "name": "search_mods", "arguments": map[string]any{"query": "again"}}})
	if r := c.readReply(); string(r.ID) != id || r.Error == nil || r.Error.Code != codeInvalidRequest {
		t.Fatalf("duplicate id: %+v", r)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": json.RawMessage(id), "reason": "user"}})
	select {
	case <-aborted:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling didn't stop the search")
	}
	for _, l := range c.rest() {
		var r reply_
		json.Unmarshal(l, &r)
		if string(r.ID) == id {
			t.Fatalf("cancelled request got a reply: %s", l)
		}
	}
}

func TestEOFFinishesRunningCalls(t *testing.T) {
	testDirs(t)
	s, f := newServer(t, Options{})
	f.searchHook = func(*http.Request) { time.Sleep(300 * time.Millisecond) }
	c := newClient(t, s)
	id := c.start("tools/call", map[string]any{"name": "search_mods", "arguments": map[string]any{"query": "sodium"}})
	found := false
	for _, l := range c.rest() {
		var r reply_
		json.Unmarshal(l, &r)
		if string(r.ID) == id {
			found = true
			if r.Result["isError"] == true {
				t.Fatalf("search failed: %s", l)
			}
		}
	}
	if !found {
		t.Fatal("the running call's reply was lost when stdin closed")
	}
}

func TestOversizedMessage(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	s.maxLine = 100 << 10
	c := newClient(t, s)
	c.line(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"x":"` + strings.Repeat("a", 200<<10) + `"}}`)
	if r := c.readReply(); r.Error == nil || r.Error.Code != codeParseError || !strings.Contains(r.Error.Message, "too large") {
		t.Fatalf("oversized message: %+v", r)
	}
	// A message near the limit, read in several chunks, still works.
	id := c.start("tools/call", map[string]any{"name": "search_mods", "arguments": map[string]any{"query": strings.Repeat("b", 90<<10)}})
	if r := c.wait(id); r.Error != nil || r.Result["isError"] == true {
		t.Fatalf("large message: %+v %v", r.Error, r.Result)
	}
}

func TestToolPanicIsReported(t *testing.T) {
	testDirs(t)
	s, _ := newServer(t, Options{})
	boom := &tool{name: "boom", title: "Boom", readOnly: true, schema: object(nil, map[string]any{}),
		run: func(context.Context, *Server, json.RawMessage) (any, error) { panic("kaboom") }}
	s.tools = append(s.tools, boom)
	s.byName["boom"] = boom
	c := newClient(t, s)
	c.fails("boom", nil, "internal error in boom: kaboom")
	if r := c.request("tools/list", nil); r.Error != nil {
		t.Fatal("the server stopped answering after a panic")
	}
}

// ---------------------------------------------------------------------------
// Tools

func TestCreatePack(t *testing.T) {
	dir := testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	args := func(path string, extra map[string]any) map[string]any {
		a := map[string]any{"pack": path, "name": "My Pack", "minecraft": "1.21.1", "loader": "fabric"}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	out := c.ok("create_pack", args(filepath.Join(dir, "mine"), map[string]any{"loaderVersion": "0.16.9", "author": "Me", "version": "0.1"}))
	path := filepath.Join(dir, "mine.mpm")
	if out["path"] != path || out["loaderVersion"] != "0.16.9" || out["mods"] != float64(0) || out["format"] != "mpm" {
		t.Fatalf("create_pack %v", out)
	}
	doc, err := core.Open(path)
	if err != nil || doc.Pack.Name != "My Pack" || doc.Pack.Author != "Me" || doc.Pack.Version != "0.1" || doc.Pack.Loader != "fabric" {
		t.Fatalf("saved pack %+v %v", doc, err)
	}
	c.fails("create_pack", args(path, map[string]any{"loaderVersion": "0.16.9"}), "already exists")
	c.ok("create_pack", args(path, map[string]any{"loaderVersion": "0.16.9", "overwrite": true}))

	// Without a loader version, the newest stable one is looked up.
	out = c.ok("create_pack", args(filepath.Join(dir, "auto.mpm"), nil))
	if out["loaderVersion"] != "0.17.2" || !strings.Contains(fmt.Sprint(out["warnings"]), "0.17.2") {
		t.Fatalf("looked-up loader version %v", out)
	}
	old := core.FabricMetaURL
	core.FabricMetaURL = strings.TrimSuffix(old, "/fabric") + "/down"
	c.fails("create_pack", args(filepath.Join(dir, "offline.mpm"), nil), "pass loaderVersion")
	core.FabricMetaURL = old

	c.fails("create_pack", args(filepath.Join(dir, "x.mpm"), map[string]any{"loader": "rift"}), "loader must be one of")
	c.fails("create_pack", args(filepath.Join(dir, "x.mrpack"), map[string]any{"loaderVersion": "1"}), "must end in .mpm")
	c.fails("create_pack", args(filepath.Join(dir, "missing", "x.mpm"), map[string]any{"loaderVersion": "1"}), "doesn't exist")
	c.fails("create_pack", args(filepath.Join(dir, "x.mpm"), map[string]any{"name": " "}), "name is required")
	c.fails("create_pack", args(filepath.Join(dir, "x.mpm"), map[string]any{"packs": "typo"}), `unknown field "packs"`)
	c.fails("create_pack", args(filepath.Join(dir, "x.mpm"), map[string]any{"overwrite": "yes"}), "invalid arguments")

	// ~ is the home folder.
	out = c.ok("create_pack", args("~/home.mpm", map[string]any{"loaderVersion": "0.16.9"}))
	home, err := os.UserHomeDir()
	if err != nil || !strings.HasPrefix(home, filepath.Dir(dir)) {
		t.Fatalf("the test's home folder isn't set: %q %v", home, err)
	}
	if out["path"] != filepath.Join(home, "home.mpm") {
		t.Fatalf("~ path %v", out["path"])
	}
}

func TestAddModAndEdit(t *testing.T) {
	dir := testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	pack := filepath.Join(dir, "p.mpm")
	c.ok("create_pack", map[string]any{"pack": pack, "name": "P", "minecraft": "1.21.1", "loader": "fabric", "loaderVersion": "0.16.9"})

	out := c.ok("add_mod", map[string]any{"pack": pack, "project": "sodium", "group": "Performance"})
	if got := names(out["added"]); !reflect.DeepEqual(got, []string{"Sodium", "Fabric API"}) {
		t.Fatalf("added %v", got)
	}
	sodium := list(out["added"])[0].(map[string]any)
	if sodium["side"] != "client" || sodium["group"] != "Performance" || sodium["modrinthId"] != "AANobbMI" || sodium["id"] == "" {
		t.Fatalf("sodium %v", sodium)
	}
	doc, _ := core.Open(pack)
	if len(doc.Pack.Mods) != 2 || doc.Pack.GroupByName("Performance") == nil {
		t.Fatalf("saved %d mods, groups %v", len(doc.Pack.Mods), doc.Pack.Groups)
	}

	out = c.ok("add_mod", map[string]any{"pack": pack, "project": "sodium"})
	if len(list(out["added"])) != 0 || !reflect.DeepEqual(names(out["alreadyInPack"]), []string{"Sodium"}) {
		t.Fatalf("adding again %v", out)
	}
	out = c.ok("add_mod", map[string]any{"pack": pack, "project": "lithium", "side": "server"})
	lith := list(out["added"])[0].(map[string]any)
	if lith["side"] != "server" || lith["sideLocked"] != true {
		t.Fatalf("lithium %v", lith)
	}
	c.fails("add_mod", map[string]any{"pack": pack, "project": "no-such-mod"}, "no-such-mod")
	c.fails("add_mod", map[string]any{"pack": pack, "project": "jei", "source": "curseforge"}, "numeric")
	c.fails("add_mod", map[string]any{"pack": pack, "project": "238222", "source": "curseforge"}, "API key")
	c.fails("add_mod", map[string]any{"pack": pack, "project": "sodium", "side": "everywhere"}, "side must be")

	out = c.ok("list_mods", map[string]any{"pack": pack, "filter": "side:client"})
	if out["total"] != float64(1) || !reflect.DeepEqual(names(out["mods"]), []string{"Sodium"}) {
		t.Fatalf("filtered list %v", out)
	}
	all := c.ok("list_mods", map[string]any{"pack": pack})
	if all["total"] != float64(3) {
		t.Fatalf("list %v", all)
	}
	page := c.ok("list_mods", map[string]any{"pack": pack, "limit": 1, "offset": 1})
	if page["total"] != float64(3) || len(list(page["mods"])) != 1 || names(page["mods"])[0] != names(all["mods"])[1] {
		t.Fatalf("page %v", page)
	}

	out = c.ok("set_side", map[string]any{"pack": pack, "mods": []string{"Sodium"}, "side": "both"})
	if m := list(out["updated"])[0].(map[string]any); m["side"] != "both" || m["sideLocked"] != true {
		t.Fatalf("set_side %v", m)
	}
	out = c.ok("set_side", map[string]any{"pack": pack, "mods": []string{sodium["id"].(string)}, "side": "client", "lock": false})
	if m := list(out["updated"])[0].(map[string]any); m["side"] != "client" || m["sideLocked"] != nil {
		t.Fatalf("set_side by id %v", m)
	}

	// A failed edit leaves the file alone.
	before, _ := os.ReadFile(pack)
	c.fails("set_side", map[string]any{"pack": pack, "mods": []string{"Sodium", "nope"}, "side": "server"}, `no mod matches "nope"`)
	if after, _ := os.ReadFile(pack); !bytes.Equal(before, after) {
		t.Fatal("a failed edit changed the file")
	}

	out = c.ok("move_to_group", map[string]any{"pack": pack, "mods": []string{"lithium"}, "group": "Server side"})
	if out["group"] != "Server side" || list(out["moved"])[0].(map[string]any)["group"] != "Server side" {
		t.Fatalf("move_to_group %v", out)
	}
	out = c.ok("move_to_group", map[string]any{"pack": pack, "mods": []string{"lithium"}, "group": ""})
	if _, ok := list(out["moved"])[0].(map[string]any)["group"]; ok {
		t.Fatalf("ungroup %v", out)
	}
	c.fails("move_to_group", map[string]any{"pack": pack, "mods": []string{"lithium"}}, "group is required")

	// Names that match more than one mod are refused.
	doc, _ = core.Open(pack)
	for _, m := range doc.Pack.Mods {
		if m.Name == "Lithium" {
			m.Name = "Sodium"
		}
	}
	if err := doc.Save(""); err != nil {
		t.Fatal(err)
	}
	c.fails("remove_mods", map[string]any{"pack": pack, "mods": []string{"sodium"}}, "matches 2 mods")

	out = c.ok("remove_mods", map[string]any{"pack": pack, "mods": []string{"fabric-api"}})
	if !reflect.DeepEqual(names(out["removed"]), []string{"Fabric API"}) || out["modsLeft"] != float64(2) {
		t.Fatalf("remove_mods %v", out)
	}
	c.fails("remove_mods", map[string]any{"pack": pack, "mods": []string{"fabric-api"}}, "no mod matches")
	c.fails("remove_mods", map[string]any{"pack": pack, "mods": []string{}}, "name at least one mod")

	out = c.ok("update_pack_info", map[string]any{"pack": pack, "name": "Renamed", "version": "2.0", "summary": ""})
	if out["name"] != "Renamed" || out["version"] != "2.0" {
		t.Fatalf("update_pack_info %v", out)
	}
	c.fails("update_pack_info", map[string]any{"pack": pack}, "pass at least one")
	c.fails("update_pack_info", map[string]any{"pack": pack, "name": " "}, "name can't be empty")
	doc, _ = core.Open(pack)
	if doc.Pack.Name != "Renamed" || doc.Pack.Version != "2.0" || len(doc.Pack.Mods) != 2 {
		t.Fatalf("final pack %+v", doc.Pack)
	}
}

func TestEditsNeedAnMPMFile(t *testing.T) {
	dir := testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	mr := writeFile(t, filepath.Join(dir, "x.mrpack"), []byte("not really"))
	c.fails("add_mod", map[string]any{"pack": mr, "project": "sodium"}, "import_pack")
	c.fails("set_side", map[string]any{"pack": filepath.Join(dir, "missing.mpm"), "mods": []string{"a"}, "side": "both"}, "missing.mpm")
	c.fails("pack_info", map[string]any{"pack": ""}, "path is required")
}

func TestAddJars(t *testing.T) {
	dir := testDirs(t)
	s, f := newServer(t, Options{})
	c := newClient(t, s)
	pack := filepath.Join(dir, "p.mpm")
	c.ok("create_pack", map[string]any{"pack": pack, "name": "P", "minecraft": "1.21.1", "loader": "fabric", "loaderVersion": "0.16.9"})

	jars := filepath.Join(dir, "jars")
	own := writeFile(t, filepath.Join(jars, "mymod-1.0.jar"), fabricJar(t, "mymod", "1.0", "client"))
	pub := fabricJar(t, "pubmod", "2.0", "*")
	writeFile(t, filepath.Join(jars, "pubmod-2.0.jar"), pub)
	f.mu.Lock()
	f.published["PUB1"] = pub
	f.mu.Unlock()

	out := c.ok("add_jars", map[string]any{"pack": pack, "files": []string{jars}, "group": "Local"})
	byFile := map[string]map[string]any{}
	for _, r := range list(out["results"]) {
		byFile[r.(map[string]any)["file"].(string)] = r.(map[string]any)
	}
	mine, linked := byFile["mymod-1.0.jar"], byFile["pubmod-2.0.jar"]
	if mine["status"] != "added" || linked["status"] != "linked" {
		t.Fatalf("add_jars %v", out)
	}
	if m := mine["mod"].(map[string]any); m["stored"] != true || m["side"] != "client" || m["source"] != "local" || m["group"] != "Local" {
		t.Fatalf("stored jar %v", m)
	}
	if m := linked["mod"].(map[string]any); m["stored"] == true || m["modrinthId"] != "P-PUB1" {
		t.Fatalf("linked jar %v", m)
	}
	doc, _ := core.Open(pack)
	if len(doc.Pack.Mods) != 2 || core.CountStored(doc.Pack.Mods) != 1 {
		t.Fatalf("saved %d mods, %d stored", len(doc.Pack.Mods), core.CountStored(doc.Pack.Mods))
	}

	out = c.ok("add_jars", map[string]any{"pack": pack, "files": []string{own}})
	if r := list(out["results"])[0].(map[string]any); r["status"] != "unchanged" {
		t.Fatalf("adding the same jar again %v", r)
	}
	newer := writeFile(t, filepath.Join(dir, "newer", "mymod-1.1.jar"), fabricJar(t, "mymod", "1.1", "client"))
	out = c.ok("add_jars", map[string]any{"pack": pack, "files": []string{newer}, "side": "both"})
	if r := list(out["results"])[0].(map[string]any); r["status"] != "updated" || r["replaced"] != "mymod-1.0.jar" || r["mod"].(map[string]any)["side"] != "both" {
		t.Fatalf("newer build %v", r)
	}

	calls := f.callCount("/")
	offline := writeFile(t, filepath.Join(dir, "off", "other-1.0.jar"), fabricJar(t, "other", "1.0", "server"))
	out = c.ok("add_jars", map[string]any{"pack": pack, "files": []string{offline}, "offline": true})
	if r := list(out["results"])[0].(map[string]any); r["status"] != "added" || f.callCount("/") != calls {
		t.Fatalf("offline add %v (%d Modrinth calls)", r, f.callCount("/")-calls)
	}
	out = c.ok("add_jars", map[string]any{"pack": pack, "files": []string{filepath.Join(dir, "nothing.jar")}})
	if r := list(out["results"])[0].(map[string]any); r["status"] != "failed" || r["error"] == "" {
		t.Fatalf("missing jar %v", r)
	}
	c.fails("add_jars", map[string]any{"pack": pack, "files": []string{}}, "files is required")
}

func TestConcurrentEditsAreKept(t *testing.T) {
	dir := testDirs(t)
	s, _ := newServer(t, Options{})
	c := newClient(t, s)
	pack := filepath.Join(dir, "p.mpm")
	c.ok("create_pack", map[string]any{"pack": pack, "name": "P", "minecraft": "1.21.1", "loader": "fabric", "loaderVersion": "0.16.9"})
	var ids []string
	for i := range 6 {
		jar := writeFile(t, filepath.Join(dir, "jars", fmt.Sprintf("mod%d-1.0.jar", i)), fabricJar(t, fmt.Sprintf("mod%d", i), "1.0", "*"))
		ids = append(ids, c.start("tools/call", map[string]any{"name": "add_jars", "arguments": map[string]any{"pack": pack, "files": []string{jar}}}))
	}
	ids = append(ids, c.start("tools/call", map[string]any{"name": "add_mod", "arguments": map[string]any{"pack": pack, "project": "lithium"}}))
	for _, id := range ids {
		if r := c.wait(id); r.Error != nil || r.Result["isError"] == true {
			t.Fatalf("call %s: %+v %v", id, r.Error, r.Result)
		}
	}
	doc, _ := core.Open(pack)
	if len(doc.Pack.Mods) != 7 {
		t.Fatalf("%d mods saved, want 7", len(doc.Pack.Mods))
	}
}

func mrpackFixture(t *testing.T, base, path string) string {
	jar := cdnJars["sodium.jar"]
	index := map[string]any{
		"formatVersion": 1, "game": "minecraft", "versionId": "1.0", "name": "Imported",
		"dependencies": map[string]string{"minecraft": "1.21.1", "fabric-loader": "0.16.9"},
		"files": []map[string]any{{"path": "mods/sodium.jar", "hashes": map[string]string{"sha1": h1(jar), "sha512": h512(jar)},
			"downloads": []string{base + "/cdn/sodium.jar"}, "fileSize": len(jar), "env": map[string]string{"client": "required", "server": "unsupported"}}},
	}
	b, _ := json.Marshal(index)
	return writeFile(t, path, zipOf(t, map[string][]byte{"modrinth.index.json": b}))
}

func TestImportAndExport(t *testing.T) {
	dir := testDirs(t)
	s, f := newServer(t, Options{})
	c := newClient(t, s)
	mr := mrpackFixture(t, f.srv.URL, filepath.Join(dir, "imported.mrpack"))

	info := c.ok("pack_info", map[string]any{"pack": mr})
	if info["format"] != "mrpack" || info["mods"] != float64(1) || info["minecraft"] != "1.21.1" || info["sides"].(map[string]any)["client"] != float64(1) {
		t.Fatalf("pack_info on .mrpack %v", info)
	}
	if got := names(c.ok("list_mods", map[string]any{"pack": mr})["mods"]); !reflect.DeepEqual(got, []string{"Sodium"}) {
		t.Fatalf("list_mods on .mrpack %v", got)
	}

	out := c.ok("import_pack", map[string]any{"file": mr, "pack": filepath.Join(dir, "imported")})
	pack := filepath.Join(dir, "imported.mpm")
	if out["path"] != pack || out["format"] != "mpm" || out["mods"] != float64(1) {
		t.Fatalf("import_pack %v", out)
	}
	c.fails("import_pack", map[string]any{"file": mr, "pack": pack}, "already exists")
	c.fails("import_pack", map[string]any{"file": pack, "pack": filepath.Join(dir, "again.mpm")}, "already an .mpm")

	c.ok("add_mod", map[string]any{"pack": pack, "project": "fabric-api"})
	writeFile(t, filepath.Join(dir, "srv", "serveronly-1.0.jar"), fabricJar(t, "serveronly", "1.0", "server"))
	c.ok("add_jars", map[string]any{"pack": pack, "files": []string{filepath.Join(dir, "srv", "serveronly-1.0.jar")}, "offline": true})

	// Client: an .mrpack next to the pack, without the common mods.
	out = c.ok("export_pack", map[string]any{"pack": pack, "target": "client"})
	clientFile := out["output"].(string)
	if filepath.Dir(clientFile) != dir || !strings.HasSuffix(clientFile, ".mrpack") || out["format"] != "mrpack" || out["mods"] != float64(1) {
		t.Fatalf("client export %v", out)
	}
	var idx struct {
		Files []struct{ Path string }
	}
	json.Unmarshal(zipEntries(t, clientFile)["modrinth.index.json"], &idx)
	if len(idx.Files) != 1 || idx.Files[0].Path != "mods/sodium.jar" {
		t.Fatalf("client .mrpack lists %+v", idx.Files)
	}
	c.fails("export_pack", map[string]any{"pack": pack, "target": "client"}, "already exists")
	if out = c.ok("export_pack", map[string]any{"pack": pack, "target": "client", "overwrite": true, "includeCommon": true}); out["mods"] != float64(2) {
		t.Fatalf("client export with common mods %v", out)
	}

	// Server: a .zip with the jars, common mods included.
	out = c.ok("export_pack", map[string]any{"pack": pack, "target": "server", "output": filepath.Join(dir, "server")})
	if out["output"] != filepath.Join(dir, "server.zip") || out["format"] != "zip" || out["includeCommon"] != true {
		t.Fatalf("server export %v", out)
	}
	entries := zipEntries(t, filepath.Join(dir, "server.zip"))
	var jarsIn []string
	for name := range entries {
		if strings.HasSuffix(name, ".jar") {
			jarsIn = append(jarsIn, filepath.Base(name))
		}
	}
	slices.Sort(jarsIn)
	if !reflect.DeepEqual(jarsIn, []string{"fabricapi.jar", "serveronly-1.0.jar"}) {
		t.Fatalf("server zip jars %v", jarsIn)
	}
	for name, b := range entries {
		if strings.HasSuffix(name, "fabricapi.jar") && !bytes.Equal(b, cdnJars["fabricapi.jar"]) {
			t.Fatal("downloaded jar has the wrong content")
		}
	}

	c.fails("export_pack", map[string]any{"pack": pack, "target": "server", "format": "zip", "output": filepath.Join(dir, "s.mrpack")}, "must end in .zip")
	c.fails("export_pack", map[string]any{"pack": mr, "target": "full", "output": mr, "overwrite": true}, "can't replace the pack itself")
	c.fails("export_pack", map[string]any{"pack": pack, "target": "everything"}, "target must be")
	c.fails("export_pack", map[string]any{"pack": pack, "target": "full", "format": "jar"}, "format must be")
}

func TestSearch(t *testing.T) {
	dir := testDirs(t)
	s, f := newServer(t, Options{})
	c := newClient(t, s)
	pack := filepath.Join(dir, "p.mpm")
	c.ok("create_pack", map[string]any{"pack": pack, "name": "P", "minecraft": "1.21.1", "loader": "fabric", "loaderVersion": "0.16.9"})
	c.ok("add_mod", map[string]any{"pack": pack, "project": "sodium"})

	out := c.ok("search_mods", map[string]any{"query": "perf", "pack": pack, "limit": 5})
	if facets := f.lastFacets(); !strings.Contains(facets, "versions:1.21.1") || !strings.Contains(facets, "categories:fabric") {
		t.Fatalf("facets %s", facets)
	}
	res := list(out["results"])
	if len(res) != 2 || out["minecraft"] != "1.21.1" || out["loader"] != "fabric" {
		t.Fatalf("search %v", out)
	}
	sod, lith := res[0].(map[string]any), res[1].(map[string]any)
	if sod["slug"] != "sodium" || sod["inPack"] != true || sod["side"] != "client" || !strings.HasPrefix(sod["url"].(string), "https://modrinth.com/") {
		t.Fatalf("sodium hit %v", sod)
	}
	if _, ok := lith["inPack"]; ok {
		t.Fatalf("lithium hit %v", lith)
	}

	c.ok("search_mods", map[string]any{"query": "shiny", "pack": pack, "type": "shader"})
	if facets := f.lastFacets(); strings.Contains(facets, "categories:fabric") {
		t.Fatalf("shader search filtered by mod loader: %s", facets)
	}
	out = c.ok("search_mods", map[string]any{"query": "perf", "source": "both"})
	if !strings.Contains(fmt.Sprint(out["warnings"]), "CurseForge was skipped") || len(list(out["results"])) != 2 {
		t.Fatalf("search both without a key %v", out)
	}
	c.fails("search_mods", map[string]any{"query": "perf", "source": "curseforge"}, "API key")
	c.fails("search_mods", map[string]any{"query": " "}, "query is required")
	c.fails("search_mods", map[string]any{"query": "x", "source": "github"}, "source must be")
}
