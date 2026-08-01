// client.go — shared HTTP helpers for remote mode and local-mode service wiring.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// The remote branches below deliberately call httpJSON on its own line before
// returning the decoded value. `return out.X, httpJSON(..., &out)` reads out.X
// and calls httpJSON in the same statement, and the spec does not order a plain
// operand against a call in the same expression — gc happens to fill out first,
// but nothing guarantees it, and the failure mode is every remote command
// silently printing an empty result.

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
	c := &http.Client{Timeout: 120 * time.Second}
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
	c := &http.Client{Timeout: 300 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var eventName string
	scanner := bufio.NewScanner(resp.Body)
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
	cleanup func()
}

var _local *localServices

func getLocal() *bootstrap.Services {
	if _local == nil {
		cfg := config.Load()
		svc, cleanup, err := bootstrap.New(cfg)
		if err != nil {
			fatalf("init: %v", err)
		}
		_local = &localServices{svc: svc, cleanup: cleanup}
	}
	return _local.svc
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

func invokeSkill(name string, args map[string]any) (map[string]any, error) {
	if isRemote() {
		var out map[string]any
		err := httpJSON("POST", "/api/skills/"+name+"/invoke",
			map[string]any{"args": args}, &out)
		return out, err
	}
	result, err := getLocal().Skills.Execute(name, args)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(result)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m, nil
}

func searchMemory(query string, k int) ([]memory.MemoryHit, error) {
	if isRemote() {
		var out struct {
			Hits []memory.MemoryHit `json:"hits"`
		}
		err := httpJSON("POST", "/api/memory/search",
			map[string]any{"query": query, "k": k}, &out)
		return out.Hits, err
	}
	res, err := getLocal().Memory.SearchMemory(query, k)
	if err != nil {
		return nil, err
	}
	return res.Hits, nil
}

func addMemory(text string, meta map[string]any) error {
	if isRemote() {
		return httpJSON("POST", "/api/memory/add",
			map[string]any{"text": text, "meta": meta}, nil)
	}
	_, err := getLocal().Memory.AddMemory(text, meta)
	return err
}

func listMemories(limit, offset int) ([]memory.MemoryHit, int, error) {
	if isRemote() {
		var out struct {
			Hits  []memory.MemoryHit `json:"hits"`
			Total int                `json:"total"`
		}
		path := fmt.Sprintf("/api/memory?limit=%d&offset=%d", limit, offset)
		err := httpJSON("GET", path, nil, &out)
		return out.Hits, out.Total, err
	}
	return getLocal().Memory.ListMemories(limit, offset)
}

func deleteMemory(id string) error {
	if isRemote() {
		return httpJSON("DELETE", "/api/memory/"+id, nil, nil)
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

func createScheduleTask(name, cron, skill string, args map[string]any, oneShot bool) error {
	if isRemote() {
		return httpJSON("POST", "/api/scheduler/tasks",
			map[string]any{"name": name, "cron_expr": cron, "skill": skill, "args": args, "one_shot": oneShot}, nil)
	}
	_, err := getLocal().Sched.Create(name, cron, skill, args, nil, oneShot)
	return err
}

func deleteScheduleTask(id int64) error {
	if isRemote() {
		return httpJSON("DELETE", fmt.Sprintf("/api/scheduler/tasks/%d", id), nil, nil)
	}
	_, err := getLocal().Sched.Delete(id)
	return err
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
