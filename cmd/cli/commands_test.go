package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/bootstrap"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/httpapi"
)

// The CLI's commands print and os.Exit, so these tests run the test binary
// itself as `omega` (TestMain dispatches to main when OMEGA_CLI_TEST_ARGS is
// set) against a real in-process gateway with a fake Ollama behind it.

func TestMain(m *testing.M) {
	if raw := os.Getenv("OMEGA_CLI_TEST_ARGS"); raw != "" {
		var args []string
		_ = json.Unmarshal([]byte(raw), &args)
		os.Args = append([]string{"omega"}, args...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeOllama answers chat with scripted envelopes (then a final answer) and
// embeddings with a word-hash vector.
type fakeOllama struct {
	mu      sync.Mutex
	replies []string
	srv     *httptest.Server
}

func newFakeOllama(t *testing.T) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case "/api/chat":
			f.mu.Lock()
			reply := `{"type":"final","answer":"fake answer"}`
			if len(f.replies) > 0 {
				reply, f.replies = f.replies[0], f.replies[1:]
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": reply}})
		case "/api/embed":
			v := make([]float64, 16)
			for _, word := range strings.Fields(strings.ToLower(fmt.Sprint(req["input"]))) {
				h := fnv.New32a()
				_, _ = h.Write([]byte(word))
				v[h.Sum32()%16]++
			}
			v[0] += 0.01
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float64{v}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOllama) script(replies ...string) {
	f.mu.Lock()
	f.replies = append([]string(nil), replies...)
	f.mu.Unlock()
}

// cliEnv is the environment for local mode against dir and the fake LLM.
func cliEnv(dir, ollamaURL string) []string {
	return []string{
		"DATA_DIR=" + dir,
		"SKILLS_DIR=" + filepath.Join(dir, "skills"),
		"AGENT_DB=" + filepath.Join(dir, "agent.sqlite3"),
		"AGENT_VECTOR_DIR=" + filepath.Join(dir, "chromem"),
		"SCHEDULER_DB=" + filepath.Join(dir, "sched.sqlite3"),
		"LLM_PROVIDER=ollama",
		"OLLAMA_URL=" + ollamaURL,
		"OLLAMA_MODEL=m",
		"OLLAMA_EMBED_MODEL=e",
		"AUTO_MEMORY_EXTRACT=false",
		"SKILLS_INSTALL_EXAMPLES=false",
		"PLAYGROUND_DISABLED=false",
		"NO_COLOR=1",
	}
}

// startGateway serves the real API over a fresh data dir.
func startGateway(t *testing.T, ollamaURL string) string {
	t.Helper()
	dir := t.TempDir()
	for _, kv := range cliEnv(dir, ollamaURL) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	cfg := config.Load()
	svc, cleanup, err := bootstrap.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Cfg: cfg, Agent: svc.Agent, Memory: svc.Memory, Skills: svc.Skills, Chat: svc.Chat,
		Scheduler: svc.Sched, Runner: svc.Runner, ToolProvider: svc.ToolProvider,
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// omega runs the CLI with args and extra env; it returns output and the exit code.
func omega(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	raw, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(), env...), "OMEGA_CLI_TEST_ARGS="+string(raw))
	cmd.Stdin = strings.NewReader("") // not a terminal
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func mustRun(t *testing.T, env []string, want string, args ...string) string {
	t.Helper()
	out, code := omega(t, env, args...)
	if code != 0 || !strings.Contains(out, want) {
		t.Fatalf("omega %v: exit %d, want %q in:\n%s", args, code, want, out)
	}
	return out
}

func TestCLIRemoteCommands(t *testing.T) {
	llm := newFakeOllama(t)
	base := startGateway(t, llm.srv.URL)
	env := []string{"OMEGA_REMOTE=" + base}

	mustRun(t, env, "omega ", "version")
	mustRun(t, env, "Usage:", "help")
	if out, code := omega(t, env, "bogus"); code == 0 || !strings.Contains(out, "unknown command") {
		t.Fatalf("bogus: %d %s", code, out)
	}

	// ask, plain and streamed.
	llm.script(`{"type":"tool_call","tool":"base64_skill","args":{"action":"encode","text":"hi"}}`, `{"type":"final","answer":"encoded it"}`)
	mustRun(t, env, "encoded it", "ask", "encode hi")
	llm.script(`{"type":"final","answer":"streamed answer"}`)
	mustRun(t, env, "streamed answer", "ask", "stream please", "--stream")
	llm.script(`{"type":"final","answer":"as json"}`)
	if out := mustRun(t, env, "as json", "ask", "q", "--json"); !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("--json output is not JSON:\n%s", out)
	}

	// skills
	mustRun(t, env, "base64_skill", "skills", "list")
	mustRun(t, env, "action", "skills", "describe", "base64_skill")
	mustRun(t, env, "aGk=", "skills", "run", "base64_skill", "--arg", "action=encode", "--arg", "text=hi")
	if _, code := omega(t, env, "skills", "describe", "no_such_skill"); code == 0 {
		t.Fatal("describe of a missing skill exited 0")
	}

	// memory
	mustRun(t, env, "", "memory", "add", "the backup server is nas01", "--namespace", "ops", "--meta", "k=v")
	mustRun(t, env, "nas01", "memory", "search", "backup server", "--namespace", "ops")
	mustRun(t, env, "nas01", "memory", "list", "--namespace", "ops")
	mustRun(t, env, "ops", "memory", "namespaces")
	exp := filepath.Join(t.TempDir(), "mem.jsonl")
	mustRun(t, env, "", "memory", "export", "-o", exp)
	if b, err := os.ReadFile(exp); err != nil || !strings.Contains(string(b), "nas01") {
		t.Fatalf("export file: %v %s", err, b)
	}

	// schedule
	mustRun(t, env, "", "schedule", "create", "--name", "tick", "--cron", "*/5 * * * *", "--skill", "datetime_skill")
	mustRun(t, env, "tick", "schedule", "list")
	mustRun(t, env, "", "schedule", "run", "1")
	mustRun(t, env, "", "schedule", "history", "1")
	mustRun(t, env, "", "schedule", "disable", "1")
	mustRun(t, env, "", "schedule", "delete", "1")
	if _, code := omega(t, env, "schedule", "create", "--name", "x"); code == 0 {
		t.Fatal("schedule create without cron exited 0")
	}

	// sessions
	mustRun(t, env, "", "session", "list")
	mustRun(t, env, "encode hi", "session", "export", "1")
}

func TestCLIRemoteApprovalWithoutTTY(t *testing.T) {
	llm := newFakeOllama(t)
	t.Setenv("APPROVAL_TIMEOUT_SEC", "1") // read when the gateway starts
	base := startGateway(t, llm.srv.URL)
	llm.script(`{"type":"tool_call","tool":"file_write","args":{"path":"x.txt","content":"x"}}`, `{"type":"final","answer":"gave up"}`)
	// No terminal: the CLI prints where to approve instead of deciding, and
	// the gateway's timeout denies the call.
	out := mustRun(t, []string{"OMEGA_REMOTE=" + base}, "needs approval", "ask", "write a file", "--stream")
	if !strings.Contains(out, "/api/approvals/") {
		t.Fatalf("no approval hint:\n%s", out)
	}
}

func TestCLILocalMode(t *testing.T) {
	llm := newFakeOllama(t)
	env := cliEnv(t.TempDir(), llm.srv.URL)
	// One reply per process: each local command is its own run.
	mustRun(t, env, "fake answer", "ask", "hello")
	mustRun(t, env, "datetime_skill", "skills", "list")
	mustRun(t, env, "", "memory", "add", "local fact", "--json")
	mustRun(t, env, "local fact", "memory", "list")
	mustRun(t, env, "", "session", "list")
}
