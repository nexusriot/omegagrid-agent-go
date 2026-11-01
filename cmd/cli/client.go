// client.go — shared HTTP helpers for remote mode and local-mode service wiring.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/bootstrap"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// remoteBase returns the gateway base URL from OMEGA_REMOTE env or empty string.
func remoteBase() string {
	return strings.TrimRight(os.Getenv("OMEGA_REMOTE"), "/")
}

// isRemote reports whether a remote URL is configured.
func isRemote() bool { return remoteBase() != "" }

// setAuth attaches the gateway bearer token from GATEWAY_API_TOKEN, which the
// gateway requires on /api/* once it is configured there.
func setAuth(req *http.Request) {
	if tok := strings.TrimSpace(os.Getenv("GATEWAY_API_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// The remote branches below deliberately call httpJSON on its own line before
// returning the decoded value. `return out.X, httpJSON(..., &out)` reads out.X
// and calls httpJSON in the same statement, and the spec does not order a plain
// operand against a call in the same expression — gc happens to fill out first,
// but nothing guarantees it, and the failure mode is every remote command
// silently printing an empty result.

// agentRunTimeout bounds a remote agent run. http.Client.Timeout covers
// reading the body too, so the old 120s (300s streaming) cut off any run that
// legitimately took longer — 25 steps at up to 120s of model time each is
// well past that — and the disconnect cancelled the run on the gateway. It
// matches the Telegram bot's budget for the same call.
const agentRunTimeout = 15 * time.Minute

// requestTimeout picks the client timeout for a remote call: agent runs get
// agentRunTimeout, everything else is a quick store lookup.
func requestTimeout(path string) time.Duration {
	if strings.HasPrefix(path, "/api/query") || strings.Contains(path, "/invoke") {
		return agentRunTimeout
	}
	return 120 * time.Second
}

// httpJSON makes a JSON request to the remote gateway and decodes the response.
func httpJSON(method, path string, reqBody, out any) error {
	base := remoteBase()
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setAuth(req)
	c := &http.Client{Timeout: requestTimeout(path)}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// httpStream hits a streaming SSE endpoint and calls onEvent for each line.
func httpStream(path string, reqBody any, onEvent func(event, data string)) error {
	base := remoteBase()
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	setAuth(req)
	c := &http.Client{Timeout: agentRunTimeout}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var eventName string
	scanner := bufio.NewScanner(resp.Body)
	// The final event inlines base64 attachments; the 64 KiB default line
	// cap rejected those as "token too long".
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventName = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			onEvent(eventName, data)
			eventName = ""
		}
	}
	return scanner.Err()
}

type localServices struct {
	svc     *bootstrap.Services
	cfg     config.Config
	cleanup func()
}

var _local *localServices

func getLocal() *bootstrap.Services {
	initLocal()
	return _local.svc
}

// localConfig returns the config local mode was wired with, so CLI commands
// honour the same environment the gateway does instead of re-deriving defaults.
func localConfig() config.Config {
	initLocal()
	return _local.cfg
}

func initLocal() {
	if _local == nil {
		cfg := config.Load()
		svc, cleanup, err := bootstrap.New(cfg)
		if err != nil {
			fatalf("init: %v", err)
		}
		_local = &localServices{svc: svc, cfg: cfg, cleanup: cleanup}
	}
}

func closeLocal() {
	if _local != nil {
		_local.cleanup()
	}
}

func listSkills() ([]skills.Skill, error) {
	if isRemote() {
		var out struct {
			Skills []skills.Skill `json:"skills"`
		}
		err := httpJSON("GET", "/api/skills", nil, &out)
		return out.Skills, err
	}
	return getLocal().Skills.List()
}

// invokeSkill runs one skill and returns its result. The playground endpoint
// answers 200 with {"result":null,"error":"..."} for a failed or unknown
// skill, which remote mode used to print as `null` and exit 0; that error is
// now returned, matching local mode. The result is returned as-is: forcing it
// into a map printed `null` for any skill that returns a string or a list.
func invokeSkill(name string, args map[string]any) (any, error) {
	if isRemote() {
		var out struct {
			Result any     `json:"result"`
			Error  *string `json:"error"`
		}
		err := httpJSON("POST", "/api/skills/"+url.PathEscape(name)+"/invoke",
			map[string]any{"args": args}, &out)
		if err != nil {
			return nil, err
		}
		if out.Error != nil && *out.Error != "" {
			return nil, errors.New(*out.Error)
		}
		return out.Result, nil
	}
	return getLocal().Skills.ExecuteContext(interruptContext(), name, args)
}

// searchMemory searches namespaces (nil = all of them).
func searchMemory(query string, k int, namespaces []string) ([]memory.MemoryHit, error) {
	if isRemote() {
		var out struct {
			Hits []memory.MemoryHit `json:"hits"`
		}
		body := map[string]any{"query": query, "k": k}
		if namespaces != nil {
			body["namespaces"] = namespaces
		}
		err := httpJSON("POST", "/api/memory/search", body, &out)
		return out.Hits, err
	}
	var res *memory.SearchResult
	var err error
	if namespaces != nil {
		res, err = getLocal().Memory.SearchMemoryIn(query, k, namespaces)
	} else {
		res, err = getLocal().Memory.SearchMemory(query, k)
	}
	if err != nil {
		return nil, err
	}
	return res.Hits, nil
}

func addMemory(text string, meta map[string]any, namespace string, ttl time.Duration) (*memory.AddResult, error) {
	if isRemote() {
		var out memory.AddResult
		err := httpJSON("POST", "/api/memory/add", map[string]any{
			"text": text, "meta": meta, "namespace": namespace, "ttl_seconds": ttl.Seconds(),
		}, &out)
		return &out, err
	}
	return getLocal().Memory.AddMemoryOpts(text, meta, memory.AddOptions{Namespace: namespace, TTL: ttl})
}

// listMemories lists one namespace, or all of them when namespace is nil.
func listMemories(limit, offset int, namespace *string) ([]memory.MemoryHit, int, error) {
	if isRemote() {
		var out struct {
			Hits  []memory.MemoryHit `json:"hits"`
			Total int                `json:"total"`
		}
		q := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
		if namespace != nil {
			q.Set("namespace", *namespace)
		}
		err := httpJSON("GET", "/api/memory?"+q.Encode(), nil, &out)
		return out.Hits, out.Total, err
	}
	return getLocal().Memory.ListMemoriesFilter(memory.ListFilter{Namespace: namespace}, limit, offset)
}

// updateMemory edits a memory; ttl nil leaves its expiry alone.
func updateMemory(id string, text *string, meta map[string]any, ttl *time.Duration) (*memory.MemoryHit, error) {
	if isRemote() {
		body := map[string]any{"meta": meta}
		if text != nil {
			body["text"] = *text
		}
		if ttl != nil {
			body["ttl_seconds"] = ttl.Seconds()
		}
		var out struct {
			Hit *memory.MemoryHit `json:"hit"`
		}
		err := httpJSON("PUT", "/api/memory/"+url.PathEscape(id), body, &out)
		return out.Hit, err
	}
	if ttl != nil {
		if meta == nil {
			meta = map[string]any{}
		}
		if *ttl > 0 {
			meta["expires_at"] = strconv.FormatFloat(float64(time.Now().Add(*ttl).UnixMilli())/1000, 'f', 3, 64)
		} else {
			meta["expires_at"] = nil
		}
	}
	hit, err := getLocal().Memory.UpdateMemory(id, text, meta)
	if err == nil && hit == nil {
		err = fmt.Errorf("memory not found: %s", id)
	}
	return hit, err
}

func memoryNamespaces() (map[string]int, error) {
	if isRemote() {
		var out struct {
			Namespaces []struct {
				Namespace string `json:"namespace"`
				Count     int    `json:"count"`
			} `json:"namespaces"`
		}
		if err := httpJSON("GET", "/api/memory/namespaces", nil, &out); err != nil {
			return nil, err
		}
		m := make(map[string]int, len(out.Namespaces))
		for _, n := range out.Namespaces {
			m[n.Namespace] = n.Count
		}
		return m, nil
	}
	return getLocal().Memory.Namespaces()
}

// exportMemories writes every memory to w as JSON Lines.
func exportMemories(w io.Writer, withEmbeddings bool) error {
	if isRemote() {
		path := "/api/memory/export"
		if withEmbeddings {
			path += "?embeddings=1"
		}
		return httpRaw("GET", path, nil, "", w)
	}
	recs, err := getLocal().Memory.ExportMemories(withEmbeddings)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// importMemories loads a JSON Lines export from r.
func importMemories(r io.Reader) (memory.ImportReport, error) {
	if isRemote() {
		var buf bytes.Buffer
		var out struct {
			Report memory.ImportReport `json:"report"`
		}
		if err := httpRaw("POST", "/api/memory/import", r, "application/x-ndjson", &buf); err != nil {
			return out.Report, err
		}
		err := json.Unmarshal(buf.Bytes(), &out)
		return out.Report, err
	}
	var recs []memory.ExportRecord
	dec := json.NewDecoder(r)
	for {
		var rec memory.ExportRecord
		if err := dec.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			return memory.ImportReport{}, fmt.Errorf("record %d: %w", len(recs)+1, err)
		}
		recs = append(recs, rec)
	}
	return getLocal().Memory.ImportMemories(recs), nil
}

// httpRaw sends body as-is and copies the response body to w. It is for the
// endpoints that do not speak a JSON object (export, import).
func httpRaw(method, path string, body io.Reader, contentType string, w io.Writer) error {
	req, err := http.NewRequest(method, remoteBase()+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	setAuth(req)
	resp, err := (&http.Client{Timeout: agentRunTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func deleteMemory(id string) error {
	if isRemote() {
		return httpJSON("DELETE", "/api/memory/"+url.PathEscape(id), nil, nil)
	}
	existed, err := getLocal().Memory.DeleteMemory(id)
	if err != nil {
		return err
	}
	if !existed {
		return fmt.Errorf("memory not found: %s", id)
	}
	return nil
}

func listSchedule() ([]*scheduler.Task, error) {
	if isRemote() {
		var out []*scheduler.Task
		err := httpJSON("GET", "/api/scheduler/tasks", nil, &out)
		return out, err
	}
	return getLocal().Sched.ListAll()
}

func createScheduleTask(spec scheduler.TaskSpec) error {
	if isRemote() {
		return httpJSON("POST", "/api/scheduler/tasks", map[string]any{
			"name": spec.Name, "cron_expr": spec.CronExpr, "skill": spec.Skill, "prompt": spec.Prompt,
			"args": spec.Args, "one_shot": spec.OneShot, "timezone": spec.Timezone,
			"max_retries": spec.MaxRetries, "retry_delay_sec": spec.RetryDelaySec,
		}, nil)
	}
	// The API rejects a malformed task with 400; local mode used to store it
	// and the task silently never fired.
	if err := scheduler.ValidateSpec(spec); err != nil {
		return err
	}
	_, err := getLocal().Sched.CreateTask(spec)
	return err
}

func setScheduleEnabled(id int64, enabled bool) error {
	if isRemote() {
		action := "disable"
		if enabled {
			action = "enable"
		}
		return httpJSON("POST", fmt.Sprintf("/api/scheduler/tasks/%d/%s", id, action), nil, nil)
	}
	ok, err := getLocal().Sched.SetEnabled(id, enabled)
	if err == nil && !ok {
		err = fmt.Errorf("task not found: %d", id)
	}
	return err
}

// runScheduleTask runs a task now. Local mode runs it in this process.
func runScheduleTask(id int64) (*scheduler.Run, error) {
	if isRemote() {
		var run scheduler.Run
		err := httpJSON("POST", fmt.Sprintf("/api/scheduler/tasks/%d/run", id), nil, &run)
		return &run, err
	}
	return getLocal().Runner.RunNow(interruptContext(), id)
}

func scheduleHistory(id int64, limit int) ([]scheduler.Run, error) {
	if isRemote() {
		var runs []scheduler.Run
		err := httpJSON("GET", fmt.Sprintf("/api/scheduler/tasks/%d/runs?limit=%d", id, limit), nil, &runs)
		return runs, err
	}
	if _, err := getLocal().Sched.Get(id); err != nil {
		return nil, fmt.Errorf("task not found: %d", id)
	}
	return getLocal().Sched.ListRuns(id, limit)
}

func deleteScheduleTask(id int64) error {
	if isRemote() {
		return httpJSON("DELETE", fmt.Sprintf("/api/scheduler/tasks/%d", id), nil, nil)
	}
	ok, err := getLocal().Sched.Delete(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task not found: %d", id)
	}
	return nil
}

func listSessions() ([]memory.SessionInfo, error) {
	if isRemote() {
		var out struct {
			Sessions []memory.SessionInfo `json:"sessions"`
		}
		err := httpJSON("GET", "/api/sessions", nil, &out)
		return out.Sessions, err
	}
	return getLocal().Memory.ListSessions(50)
}

func exportSession(id int) ([]memory.StoredMessage, error) {
	if isRemote() {
		var out struct {
			Messages []memory.StoredMessage `json:"messages"`
		}
		err := httpJSON("GET", fmt.Sprintf("/api/sessions/%d/messages?limit=10000", id), nil, &out)
		return out.Messages, err
	}
	return getLocal().Memory.ListMessages(id, 10000, 0)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "omega: "+format+"\n", args...)
	os.Exit(1)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// isTTY reports whether stdout is a terminal.
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// color helpers — only emit ANSI when stdout is a TTY.
func colorize(code, s string) string {
	if !isTTY() {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func grey(s string) string   { return colorize("2", s) }
func cyan(s string) string   { return colorize("36", s) }
func green(s string) string  { return colorize("32", s) }
func yellow(s string) string { return colorize("33", s) }
func red(s string) string    { return colorize("31", s) }
