package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// openapi turns any configured OpenAPI (3.x or Swagger 2.0) spec into
// callable operations: search the operations, read one's parameters, call
// it. One skill instead of a tool per operation keeps a 400-operation API
// from flooding the catalogue; tool retrieval finds the skill, and the
// operations search finds the endpoint.

func OpenAPISchema() Skill {
	return Skill{Name: "openapi", Description: "Call a REST API described by a configured OpenAPI spec (SKILL_OPENAPI). " +
		"action=operations searches the API's endpoints (match = what you want to do), describe shows one operation's parameters, " +
		"call runs it with params and body. Calls that change data (POST/PUT/PATCH/DELETE) need the user's approval; apis lists the configured APIs.",
		Parameters: map[string]Param{
			"action":    {Type: "string", Description: "operations (default) | describe | call | apis", Required: false},
			"api":       {Type: "string", Description: "Configured API name; default: the first one", Required: false},
			"match":     {Type: "string", Description: "operations: keywords, e.g. 'list open issues'", Required: false},
			"operation": {Type: "string", Description: "describe/call: operationId, or 'METHOD /path'", Required: false},
			"params":    {Type: "object", Description: "call: path, query and header parameters by name", Required: false},
			"body":      {Type: "object", Description: "call: JSON request body", Required: false},
		}}
}

// OpenAPIConfig lists the APIs and how to reach them.
type OpenAPIConfig struct {
	Specs     map[string]string // name → spec URL, absolute path, or workspace path
	Order     []string
	Auth      map[string]string // name → "Bearer x" / "Basic x" (Authorization) or "Header-Name: value"
	BaseURL   map[string]string // name → override for the spec's server URL
	Workspace WorkspaceConfig
	Timeout   time.Duration
}

type apiOperation struct {
	ID       string
	Method   string
	Path     string
	Summary  string
	Desc     string
	Tags     []string
	Params   []apiParam
	Body     map[string]any // resolved request-body schema (JSON), nil when none
	BodyReq  bool
	Response map[string]any // resolved 2xx response schema, nil when none
}

type apiParam struct {
	Name     string
	In       string // path | query | header
	Required bool
	Type     string
	Desc     string
	Enum     []any
}

type apiSpec struct {
	Title   string
	Version string
	BaseURL string
	Ops     []*apiOperation
	loaded  time.Time
}

const (
	specMaxBytes  = 16 << 20
	specCacheTTL  = 10 * time.Minute
	apiMaxResp    = 4 << 20
	apiMaxResJSON = 64 << 10
)

var (
	specMu    sync.Mutex
	specCache = map[string]*apiSpec{}
)

func (c OpenAPIConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

func (c OpenAPIConfig) pick(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		if len(c.Order) == 0 {
			return "", fmt.Errorf("no API is configured (set SKILL_OPENAPI=name=https://…/openapi.json)")
		}
		return c.Order[0], nil
	}
	if _, ok := c.Specs[name]; !ok {
		return "", fmt.Errorf("unknown api %q (configured: %s)", name, strings.Join(c.Order, ", "))
	}
	return name, nil
}

// spec loads (or returns the cached) parsed spec for api.
func (c OpenAPIConfig) spec(ctx context.Context, api string) (*apiSpec, error) {
	src := c.Specs[api]
	key := api + "\x00" + src
	specMu.Lock()
	if s, ok := specCache[key]; ok && time.Since(s.loaded) < specCacheTTL {
		specMu.Unlock()
		return s, nil
	}
	specMu.Unlock()

	raw, err := c.readSpec(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("api %s: reading spec: %v", api, err)
	}
	s, err := parseOpenAPI(raw, src)
	if err != nil {
		return nil, fmt.Errorf("api %s: %v", api, err)
	}
	if b := strings.TrimSpace(c.BaseURL[api]); b != "" {
		s.BaseURL = strings.TrimRight(b, "/")
	}
	if s.BaseURL == "" {
		return nil, fmt.Errorf("api %s: the spec names no server URL; set SKILL_OPENAPI_%s_BASE_URL", api, strings.ToUpper(api))
	}
	s.loaded = time.Now()
	specMu.Lock()
	specCache[key] = s
	specMu.Unlock()
	return s, nil
}

func (c OpenAPIConfig) readSpec(ctx context.Context, src string) ([]byte, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json, application/yaml;q=0.9, */*;q=0.5")
		resp, err := (&http.Client{Timeout: c.timeout()}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return readSpecCapped(resp.Body, specMaxBytes)
	}
	// An absolute path is the operator's (it came from the config); a
	// relative one is read from the workspace.
	if filepath.IsAbs(src) {
		f, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readSpecCapped(f, specMaxBytes)
	}
	root, err := c.Workspace.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := relPath(src)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readSpecCapped(f, specMaxBytes)
}

func readSpecCapped(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("larger than %d MB", n>>20)
	}
	return b, nil
}

// parseOpenAPI reads a JSON or YAML OpenAPI 3.x / Swagger 2.0 document.
func parseOpenAPI(raw []byte, src string) (*apiSpec, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not JSON or YAML: %v", err)
	}
	doc = normalizeYAML(doc).(map[string]any)
	info, _ := doc["info"].(map[string]any)
	s := &apiSpec{Title: fmt.Sprint(info["title"]), Version: fmt.Sprint(info["version"])}
	_, v3 := doc["openapi"]
	_, v2 := doc["swagger"]
	if !v2 && !v3 {
		return nil, fmt.Errorf("not an OpenAPI document (no openapi/swagger field)")
	}
	r := refResolver{doc: doc}
	if v3 {
		if servers, _ := doc["servers"].([]any); len(servers) > 0 {
			sv, _ := servers[0].(map[string]any)
			u, _ := sv["url"].(string)
			if vars, ok := sv["variables"].(map[string]any); ok {
				for k, v := range vars {
					vm, _ := v.(map[string]any)
					u = strings.ReplaceAll(u, "{"+k+"}", fmt.Sprint(vm["default"]))
				}
			}
			s.BaseURL = resolveAgainst(src, u)
		} else {
			s.BaseURL = resolveAgainst(src, "/")
		}
	} else {
		host, _ := doc["host"].(string)
		basePath, _ := doc["basePath"].(string)
		scheme := "https"
		if sc, _ := doc["schemes"].([]any); len(sc) > 0 {
			scheme = fmt.Sprint(sc[0])
			for _, x := range sc {
				if x == "https" {
					scheme = "https"
				}
			}
		}
		if host != "" {
			s.BaseURL = scheme + "://" + host + basePath
		} else {
			s.BaseURL = resolveAgainst(src, basePath)
		}
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")

	paths, _ := doc["paths"].(map[string]any)
	pathKeys := make([]string, 0, len(paths))
	for p := range paths {
		pathKeys = append(pathKeys, p)
	}
	sort.Strings(pathKeys)
	for _, p := range pathKeys {
		item, _ := r.deref(paths[p]).(map[string]any)
		shared := r.params(item["parameters"], v2)
		for _, m := range []string{"get", "put", "post", "delete", "patch", "head", "options"} {
			opRaw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			op := &apiOperation{Method: strings.ToUpper(m), Path: p}
			op.ID, _ = opRaw["operationId"].(string)
			if op.ID == "" {
				op.ID = op.Method + " " + p
			}
			op.Summary, _ = opRaw["summary"].(string)
			op.Desc, _ = opRaw["description"].(string)
			for _, t := range asSlice(opRaw["tags"]) {
				op.Tags = append(op.Tags, fmt.Sprint(t))
			}
			// Operation parameters override path-level ones with the same name+in.
			byKey := map[string]apiParam{}
			var order []string
			for _, prm := range append(shared, r.params(opRaw["parameters"], v2)...) {
				k := prm.In + ":" + prm.Name
				if _, ok := byKey[k]; !ok {
					order = append(order, k)
				}
				byKey[k] = prm
			}
			for _, k := range order {
				if prm := byKey[k]; prm.In != "body" && prm.In != "formData" && prm.In != "cookie" {
					op.Params = append(op.Params, prm)
				} else if prm.In == "body" {
					op.BodyReq = prm.Required
				}
			}
			if v2 {
				for _, raw := range asSlice(opRaw["parameters"]) {
					pm, _ := r.deref(raw).(map[string]any)
					if pm["in"] == "body" {
						op.Body, _ = r.deep(pm["schema"], 0).(map[string]any)
					}
				}
			} else if rb, ok := r.deref(opRaw["requestBody"]).(map[string]any); ok {
				op.BodyReq, _ = rb["required"].(bool)
				if content, ok := rb["content"].(map[string]any); ok {
					for ct, media := range content {
						if strings.Contains(ct, "json") {
							mm, _ := media.(map[string]any)
							op.Body, _ = r.deep(mm["schema"], 0).(map[string]any)
							break
						}
					}
				}
			}
			op.Response = r.successSchema(opRaw["responses"], v2)
			s.Ops = append(s.Ops, op)
		}
	}
	if len(s.Ops) == 0 {
		return nil, fmt.Errorf("the spec defines no operations")
	}
	return s, nil
}

// normalizeYAML turns yaml.v3's map[string]any trees into JSON shapes (it
// already uses string keys for mappings with string keys; numeric keys such
// as response codes become strings here).
func normalizeYAML(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeYAML(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = normalizeYAML(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = normalizeYAML(e)
		}
		return x
	}
	return v
}

func resolveAgainst(src, u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	base, err := url.Parse(src)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "" // a file spec with a relative server URL: needs BASE_URL
	}
	ref, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// refResolver follows local "#/..." references.
type refResolver struct{ doc map[string]any }

func (r refResolver) deref(v any) any {
	for i := 0; i < 10; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return v
		}
		v = r.lookup(ref)
	}
	return v
}

func (r refResolver) lookup(ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil // remote refs are not followed
	}
	var cur any = r.doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// deep resolves refs to a bounded depth and drops noise, for showing a
// schema to the model.
func (r refResolver) deep(v any, depth int) any {
	v = r.deref(v)
	if depth > 4 {
		if m, ok := v.(map[string]any); ok && m["type"] != nil {
			return map[string]any{"type": m["type"]}
		}
		return "…"
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			switch k {
			case "example", "examples", "xml", "externalDocs", "deprecated", "readOnly", "x-github":
				continue
			case "description":
				if s, ok := e.(string); ok {
					out[k] = clipRunes(s, 160)
				}
				continue
			}
			out[k] = r.deep(e, depth+1)
		}
		return out
	case []any:
		if len(x) > 20 {
			x = x[:20]
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.deep(e, depth+1)
		}
		return out
	}
	return v
}

func (r refResolver) params(v any, v2 bool) []apiParam {
	var out []apiParam
	for _, raw := range asSlice(v) {
		pm, _ := r.deref(raw).(map[string]any)
		if pm == nil {
			continue
		}
		p := apiParam{}
		p.Name, _ = pm["name"].(string)
		p.In, _ = pm["in"].(string)
		p.Required, _ = pm["required"].(bool)
		p.Desc, _ = pm["description"].(string)
		sch := pm
		if !v2 {
			sch, _ = r.deref(pm["schema"]).(map[string]any)
		}
		if sch != nil {
			p.Type, _ = sch["type"].(string)
			p.Enum = asSlice(sch["enum"])
		}
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r refResolver) successSchema(v any, v2 bool) map[string]any {
	resps, _ := v.(map[string]any)
	for _, code := range []string{"200", "201", "202", "2XX", "default"} {
		rr, ok := r.deref(resps[code]).(map[string]any)
		if !ok {
			continue
		}
		if v2 {
			m, _ := r.deep(rr["schema"], 1).(map[string]any)
			return m
		}
		content, _ := rr["content"].(map[string]any)
		for ct, media := range content {
			if strings.Contains(ct, "json") {
				mm, _ := media.(map[string]any)
				m, _ := r.deep(mm["schema"], 1).(map[string]any)
				return m
			}
		}
		return nil
	}
	return nil
}

func (s *apiSpec) find(op string) (*apiOperation, error) {
	op = strings.TrimSpace(op)
	if op == "" {
		return nil, fmt.Errorf("operation is required (an operationId or 'METHOD /path'; use action=operations to search)")
	}
	for _, o := range s.Ops {
		if strings.EqualFold(o.ID, op) {
			return o, nil
		}
	}
	if m, p, ok := strings.Cut(op, " "); ok {
		for _, o := range s.Ops {
			if strings.EqualFold(o.Method, m) && o.Path == strings.TrimSpace(p) {
				return o, nil
			}
		}
	}
	return nil, fmt.Errorf("no operation %q (use action=operations to search)", op)
}

var camelRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func opWords(o *apiOperation) []string {
	text := camelRE.ReplaceAllString(o.ID, "$1 $2") + " " + o.Path + " " + o.Summary + " " + strings.Join(o.Tags, " ")
	f := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return f
}

// verbMethod maps common intent words to the HTTP method that does it.
var verbMethod = map[string]string{
	"list": "GET", "get": "GET", "show": "GET", "find": "GET", "search": "GET", "read": "GET", "fetch": "GET",
	"create": "POST", "add": "POST", "new": "POST", "post": "POST", "send": "POST",
	"update": "PATCH", "edit": "PATCH", "change": "PATCH", "set": "PUT", "replace": "PUT",
	"delete": "DELETE", "remove": "DELETE",
}

func (s *apiSpec) search(match string, limit int) []*apiOperation {
	words := strings.FieldsFunc(strings.ToLower(match), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	type scored struct {
		op    *apiOperation
		score float64
	}
	var hits []scored
	for _, o := range s.Ops {
		if len(words) == 0 {
			hits = append(hits, scored{o, 0})
			continue
		}
		ow := opWords(o)
		score := 0.0
		for _, w := range words {
			if len(w) < 2 {
				continue
			}
			if m, ok := verbMethod[w]; ok && (m == o.Method || (m == "PATCH" && o.Method == "PUT")) {
				score += 0.5
			}
			for _, x := range ow {
				if x == w || (len(w) >= 4 && (strings.HasPrefix(x, w) || strings.HasPrefix(w, x) && len(x) >= 4)) {
					score++
					break
				}
			}
		}
		if score > 0 {
			hits = append(hits, scored{o, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]*apiOperation, 0, limit)
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].op)
	}
	return out
}

func opSummary(o *apiOperation) map[string]any {
	m := map[string]any{"operation": o.ID, "method": o.Method, "path": o.Path}
	if o.Summary != "" {
		m["summary"] = clipRunes(o.Summary, 160)
	} else if o.Desc != "" {
		m["summary"] = clipRunes(o.Desc, 160)
	}
	return m
}

// mutating reports whether the method changes server state.
func mutating(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	return true
}

// OpenAPINeedsApproval gates every call that is not a read. When the
// operation cannot be resolved the call is gated too: the gate errs closed.
func OpenAPINeedsApproval(c OpenAPIConfig) func(args map[string]any) bool {
	return func(args map[string]any) bool {
		if a := strings.ToLower(strings.TrimSpace(str(args, "action"))); a != "call" {
			return false
		}
		api, err := c.pick(str(args, "api"))
		if err != nil {
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
		defer cancel()
		s, err := c.spec(ctx, api)
		if err != nil {
			return true
		}
		op, err := s.find(str(args, "operation"))
		return err != nil || mutating(op.Method)
	}
}

func OpenAPIContext(c OpenAPIConfig) ContextExecutor {
	return func(ctx context.Context, args map[string]any) (any, error) {
		action := strings.ToLower(strings.TrimSpace(str(args, "action")))
		if action == "apis" {
			out := []map[string]any{}
			for _, name := range c.Order {
				e := map[string]any{"api": name}
				if s, err := c.spec(ctx, name); err != nil {
					e["error"] = err.Error()
				} else {
					e["title"], e["version"], e["operations"], e["base_url"] = s.Title, s.Version, len(s.Ops), s.BaseURL
				}
				out = append(out, e)
			}
			return map[string]any{"apis": out}, nil
		}
		api, err := c.pick(str(args, "api"))
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		s, err := c.spec(ctx, api)
		if err != nil {
			return map[string]any{"error": err.Error()}, nil
		}
		switch action {
		case "", "operations", "search":
			ops := s.search(str(args, "match"), 25)
			list := make([]map[string]any, len(ops))
			for i, o := range ops {
				list[i] = opSummary(o)
			}
			res := map[string]any{"api": api, "title": s.Title, "total_operations": len(s.Ops), "operations": list}
			if len(list) == 0 {
				res["note"] = "nothing matched; try other words, or omit match to list operations"
			} else {
				res["note"] = "use action=describe to see an operation's parameters before calling it"
			}
			return res, nil
		case "describe":
			op, err := s.find(str(args, "operation"))
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			return describeOp(api, op), nil
		case "call":
			op, err := s.find(str(args, "operation"))
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			return c.call(ctx, api, s, op, args)
		}
		return map[string]any{"error": "action must be operations, describe, call or apis"}, nil
	}
}

func describeOp(api string, op *apiOperation) map[string]any {
	res := opSummary(op)
	res["api"] = api
	if op.Desc != "" {
		res["description"] = clipRunes(op.Desc, 600)
	}
	params := make([]map[string]any, 0, len(op.Params))
	for _, p := range op.Params {
		e := map[string]any{"name": p.Name, "in": p.In, "required": p.Required || p.In == "path"}
		if p.Type != "" {
			e["type"] = p.Type
		}
		if p.Desc != "" {
			e["description"] = clipRunes(p.Desc, 200)
		}
		if len(p.Enum) > 0 {
			e["enum"] = p.Enum
		}
		params = append(params, e)
	}
	res["params"] = params
	if op.Body != nil {
		res["body_schema"] = op.Body
		res["body_required"] = op.BodyReq
	}
	if op.Response != nil {
		res["response_schema"] = op.Response
	}
	res["needs_approval"] = mutating(op.Method)
	return res
}

// blockedHeaders may not be set through params: the configured credentials
// and the request framing are not the model's to change.
var blockedHeaders = map[string]bool{"authorization": true, "cookie": true, "host": true,
	"content-length": true, "proxy-authorization": true, "transfer-encoding": true}

func (c OpenAPIConfig) call(ctx context.Context, api string, s *apiSpec, op *apiOperation, args map[string]any) (any, error) {
	given, _ := args["params"].(map[string]any)
	if given == nil {
		given = map[string]any{}
	}
	known := map[string]apiParam{}
	for _, p := range op.Params {
		known[p.Name] = p
	}
	for k := range given {
		if _, ok := known[k]; !ok {
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			return map[string]any{"error": fmt.Sprintf("unknown parameter %q for %s (parameters: %s)", k, op.ID, strings.Join(names, ", "))}, nil
		}
	}
	p := op.Path
	q := url.Values{}
	hdr := http.Header{}
	for _, prm := range op.Params {
		v, ok := given[prm.Name]
		if !ok || v == nil {
			if prm.Required || prm.In == "path" {
				return map[string]any{"error": fmt.Sprintf("missing required %s parameter %q", prm.In, prm.Name), "operation": describeOp(api, op)}, nil
			}
			continue
		}
		switch prm.In {
		case "path":
			sv := paramString(v)
			if sv == "" || sv == "." || sv == ".." {
				return map[string]any{"error": fmt.Sprintf("bad value for path parameter %q", prm.Name)}, nil
			}
			p = strings.ReplaceAll(p, "{"+prm.Name+"}", url.PathEscape(sv))
		case "query":
			if list, ok := v.([]any); ok {
				for _, e := range list {
					q.Add(prm.Name, paramString(e))
				}
			} else {
				q.Set(prm.Name, paramString(v))
			}
		case "header":
			if blockedHeaders[strings.ToLower(prm.Name)] {
				return map[string]any{"error": fmt.Sprintf("header %q cannot be set", prm.Name)}, nil
			}
			hdr.Set(prm.Name, paramString(v))
		}
	}
	target := s.BaseURL + p
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var body io.Reader
	if b, ok := args["body"]; ok && b != nil {
		if op.Body == nil && !mutating(op.Method) {
			return map[string]any{"error": op.ID + " takes no request body"}, nil
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return map[string]any{"error": "body: " + err.Error()}, nil
		}
		body = bytes.NewReader(raw)
		hdr.Set("Content-Type", "application/json")
	} else if op.BodyReq {
		return map[string]any{"error": "body is required for " + op.ID, "operation": describeOp(api, op)}, nil
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, target, body)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	req.Header = hdr
	req.Header.Set("Accept", "application/json, */*;q=0.5")
	req.Header.Set("User-Agent", "omegagrid-agent")
	if a := strings.TrimSpace(c.Auth[api]); a != "" {
		if name, val, ok := strings.Cut(a, ":"); ok && !strings.Contains(name, " ") {
			req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(val))
		} else {
			req.Header.Set("Authorization", a)
		}
	}
	t0 := time.Now()
	resp, err := (&http.Client{Timeout: c.timeout()}).Do(req)
	if err != nil {
		return map[string]any{"error": err.Error(), "operation": op.ID}, nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, apiMaxResp+1))
	if err != nil {
		return map[string]any{"error": err.Error(), "operation": op.ID, "status": resp.StatusCode}, nil
	}
	res := map[string]any{
		"api": api, "operation": op.ID, "method": op.Method, "url": redactQuery(target),
		"status": resp.StatusCode, "elapsed_ms": time.Since(t0).Milliseconds(),
	}
	if len(raw) > apiMaxResp {
		raw = raw[:apiMaxResp]
		res["truncated"] = true
	}
	ct := resp.Header.Get("Content-Type")
	res["content_type"] = ct
	var parsed any
	if strings.Contains(ct, "json") && len(raw) <= apiMaxResJSON && json.Unmarshal(raw, &parsed) == nil {
		res["body"] = parsed
	} else {
		text := string(bytes.ToValidUTF8(raw, []byte("?")))
		res["body"] = clipRunes(text, 12000)
		if len(raw) > apiMaxResJSON && strings.Contains(ct, "json") {
			res["note"] = "large JSON response shown as text; narrow the request (filters, per_page) for structured output"
		}
	}
	if resp.StatusCode >= 400 {
		res["error"] = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res, nil
}

func paramString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprint(x)
	case bool:
		return fmt.Sprint(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// redactQuery hides query values that look like credentials.
func redactQuery(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := pu.Query()
	for k := range q {
		l := strings.ToLower(k)
		if strings.Contains(l, "key") || strings.Contains(l, "token") || strings.Contains(l, "secret") || strings.Contains(l, "password") {
			q.Set(k, "REDACTED")
		}
	}
	pu.RawQuery = q.Encode()
	return pu.String()
}
