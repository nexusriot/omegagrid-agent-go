// Package skills provides an in-process skill registry backed by Go
// implementations of every Python sidecar skill.  The public API (List /
// Execute) is unchanged from the former HTTP-based client.
package skills

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/builtin"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/examples"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/markdown"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills/netguard"
)

// Param describes a single skill parameter.
type Param struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Skill is the public schema returned by List().
type Skill struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  map[string]Param `json:"parameters"`
	Body        string           `json:"body,omitempty"`
	// Kind is "builtin", "markdown" (editable, from SKILLS_DIR) or "remote"
	// (an MCP tool), filled in by List.
	Kind string `json:"kind,omitempty"`
	// RequiresApproval marks a skill the agent must not run without a human
	// yes (see NeedsApproval), filled in by List.
	RequiresApproval bool `json:"requires_approval,omitempty"`
}

// Client is the in-process skill manager.  Callers use the same List() and
// Execute() methods they used with the former HTTP sidecar client.
type Client struct {
	reg       *Registry
	skillsDir string

	approvalMu sync.RWMutex
	approval   []string // APPROVAL_SKILLS patterns
}

// New builds the skill registry from built-in skills and any *.md files in cfg.SkillsDir.
func New(cfg config.Config) (*Client, error) {
	reg := newRegistry()
	c := &Client{reg: reg, skillsDir: cfg.SkillsDir}
	if cfg.ApprovalSkills != nil {
		c.SetApprovalPatterns(cfg.ApprovalSkills)
	} else {
		c.SetApprovalPatterns(DefaultApprovalSkills)
	}

	// Outbound policy for every skill that dials (http_request, port_scan,
	// markdown endpoints, ...). Process-wide: the guard lives in the dialers.
	netguard.SetAllowPrivate(!cfg.SkillBlockPrivateNetworks)

	registerBuiltins(reg, cfg)

	reg.registerReserved(skillCreatorSchema(), builtin.Lift(skillCreatorFunc(cfg.SkillsDir, reg)))

	// Ship the bundled pipeline examples into SKILLS_DIR once.
	if cfg.SkillExamplesSeeded && cfg.SkillsDir != "" {
		if written, err := examples.Install(cfg.SkillsDir); err != nil {
			log.Printf("skills: installing example skills: %v", err)
		} else if len(written) > 0 {
			log.Printf("skills: installed example skills %v into %s", written, cfg.SkillsDir)
		}
	}

	if err := c.loadMarkdownSkills(); err != nil {
		return nil, fmt.Errorf("load markdown skills: %w", err)
	}

	return c, nil
}

func (c *Client) loadMarkdownSkills() error {
	loaded, err := markdown.LoadDir(c.skillsDir)
	if err != nil {
		return err
	}
	for _, sk := range loaded {
		if c.reg.isReserved(sk.Schema.Name) {
			log.Printf("skills: ignoring markdown skill %q in %s: name collides with a built-in skill",
				sk.Schema.Name, c.skillsDir)
			continue
		}
		c.reg.registerMarkdown(sk)
	}
	return nil
}

// mdToSkill converts markdown.SkillSchema → skills.Skill.
func mdToSkill(ms markdown.SkillSchema) Skill {
	params := make(map[string]Param, len(ms.Parameters))
	for k, v := range ms.Parameters {
		params[k] = Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}
	return Skill{Name: ms.Name, Description: ms.Description, Parameters: params, Body: ms.Body}
}

// List returns all registered skill schemas.  Called on every agent run so
// hot-registered skills (skill_creator) are immediately visible.
func (c *Client) List() ([]Skill, error) {
	list := c.reg.list()
	for i := range list {
		list[i].RequiresApproval = c.NeedsApproval(list[i].Name)
	}
	return list, nil
}

// Execute runs a skill by name with no cancellation.
func (c *Client) Execute(name string, args map[string]any) (any, error) {
	return c.reg.execute(context.Background(), name, args)
}

// ExecuteContext runs a skill by name; cancelling ctx stops a skill that is
// waiting on the network or a process (and every nested pipeline step).
func (c *Client) ExecuteContext(ctx context.Context, name string, args map[string]any) (any, error) {
	return c.reg.execute(ctx, name, args)
}

// Register adds a skill to the registry at runtime. Used to register tools
// discovered from remote MCP servers so they become callable like any
// built-in skill. A name that is already taken is refused: the remote server
// picks the tool half of "<server>_<tool>", so a server named "http" offering
// "request" used to replace the built-in http_request — and with it the
// private-network and enable gates the built-in enforces.
func (c *Client) Register(name, description string, params map[string]Param, exec builtin.ContextExecutor) error {
	if !c.reg.registerNew(Skill{Name: name, Description: description, Parameters: params}, exec) {
		return fmt.Errorf("skill name %q is already taken", name)
	}
	return nil
}

// Unregister removes a skill added with Register. Built-ins and markdown
// skills are refused (false): only runtime registrations may be withdrawn.
func (c *Client) Unregister(name string) bool {
	return c.reg.unregisterRuntime(name)
}

// toSkill converts a builtin.Skill to the public skills.Skill type. Body stays
// empty: only markdown skills carry an instructions block.
func toSkill(b builtin.Skill) Skill {
	params := make(map[string]Param, len(b.Parameters))
	for k, v := range b.Parameters {
		params[k] = Param{Type: v.Type, Description: v.Description, Required: v.Required}
	}
	return Skill{Name: b.Name, Description: b.Description, Parameters: params}
}

func registerBuiltins(reg *Registry, cfg config.Config) {
	t := cfg.SkillHTTPTimeout
	// Every built-in name is reserved against markdown skills.
	register := reg.registerReserved

	register(toSkill(builtin.WeatherSchema()), builtin.WeatherContext(t))
	register(toSkill(builtin.HttpRequestSchema()), builtin.HttpRequestContext(t))
	register(toSkill(builtin.WebScrapeSchema()), builtin.WebScrapeContext(t))
	register(toSkill(builtin.HttpHealthSchema()), builtin.HttpHealthContext())
	register(toSkill(builtin.IpInfoSchema()), builtin.IpInfoContext(t))

	register(toSkill(builtin.DnsLookupSchema()), builtin.DnsLookupContext())
	register(toSkill(builtin.PingCheckSchema()), builtin.PingCheckContext())
	register(toSkill(builtin.PortScanSchema()), builtin.PortScanContext())
	register(toSkill(builtin.WhoisLookupSchema()), builtin.WhoisLookupContext())
	register(toSkill(builtin.TlsProbeSchema()), builtin.TlsProbeContext())
	register(toSkill(builtin.HttpHeadersSchema()), builtin.HttpHeadersContext())
	register(toSkill(builtin.BannerGrabSchema()), builtin.BannerGrabContext())
	register(toSkill(builtin.PtrLookupSchema()), builtin.PtrLookupContext())
	register(toSkill(builtin.EmailAuthSchema()), builtin.EmailAuthContext())

	register(toSkill(builtin.Base64Schema()), builtin.Lift(builtin.Base64()))
	register(toSkill(builtin.HashSchema()), builtin.Lift(builtin.Hash()))
	register(toSkill(builtin.UuidGenSchema()), builtin.Lift(builtin.UuidGen()))
	register(toSkill(builtin.PasswordGenSchema()), builtin.Lift(builtin.PasswordGen()))
	register(toSkill(builtin.CidrCalcSchema()), builtin.Lift(builtin.CidrCalc()))
	register(toSkill(builtin.JwtInspectSchema()), builtin.Lift(builtin.JwtInspect()))

	register(toSkill(builtin.DateTimeSchema()), builtin.Lift(builtin.DateTime()))
	register(toSkill(builtin.MathEvalSchema()), builtin.Lift(builtin.MathEval()))
	register(toSkill(builtin.CronScheduleSchema()), builtin.Lift(builtin.CronSchedule()))
	register(toSkill(builtin.ReminderSchema()), builtin.Lift(builtin.Reminder()))

	register(toSkill(builtin.ShellCommandSchema()), builtin.ShellCommandWith(builtin.ShellConfig{
		Enabled:         cfg.SkillShellEnabled,
		Sandbox:         cfg.SkillShellSandbox,
		SandboxNetwork:  cfg.SkillShellSandboxNetwork,
		SandboxWritable: cfg.SkillShellSandboxWritable,
	}))
	register(toSkill(builtin.SshCommandSchema()), builtin.SshCommandContext(builtin.SSHConfig{
		Enabled:       cfg.SkillSSHEnabled,
		PrivKey:       cfg.SkillSSHPrivKey,
		DefaultUser:   cfg.SkillSSHDefaultUser,
		IdentFile:     cfg.SkillSSHIdentFile,
		KnownHosts:    cfg.SkillSSHKnownHosts,
		HostKeyPolicy: cfg.SkillSSHHostKeyPolicy,
	}))

	register(toSkill(builtin.QrGenerateSchema()), builtin.Lift(builtin.QrGenerate()))

	register(toSkill(builtin.RssFetchSchema()), builtin.RssFetchContext(t))
	register(toSkill(builtin.NotifySchema()), builtin.NotifyContext(builtin.NotifyConfig{
		TelegramToken:       cfg.TelegramBotToken,
		TelegramChats:       cfg.NotifyTelegramChats,
		TelegramDefaultChat: cfg.NotifyTelegramDefaultChat,
		EmailTo:             cfg.NotifyEmailTo,
		SMTPHost:            cfg.NotifySMTPHost,
		SMTPPort:            cfg.NotifySMTPPort,
		SMTPUser:            cfg.NotifySMTPUser,
		SMTPPassword:        cfg.NotifySMTPPassword,
		SMTPFrom:            cfg.NotifySMTPFrom,
		Webhooks:            builtin.ParseWebhooks(cfg.NotifyWebhooks),
	}))

	register(toSkill(builtin.HttpTimingSchema()), builtin.HttpTimingContext())
	register(toSkill(builtin.DnsCompareSchema()), builtin.DnsCompareContext())
	register(toSkill(builtin.RdapLookupSchema()), builtin.RdapLookupContext(t))
	register(toSkill(builtin.TracerouteSchema()), builtin.TracerouteContext())
	register(toSkill(builtin.SmtpCheckSchema()), builtin.SmtpCheckContext())
	register(toSkill(builtin.WakeOnLanSchema()), builtin.WakeOnLanContext())
	register(toSkill(builtin.SubnetSweepSchema()), builtin.SubnetSweepContext())

	ws := builtin.WorkspaceConfig{Dir: cfg.SkillWorkspaceDir, MaxBytes: int64(cfg.SkillWorkspaceMaxMB * (1 << 20))}
	register(toSkill(builtin.JsonQuerySchema()), builtin.Lift(builtin.JsonQuery()))
	register(toSkill(builtin.TimeConvertSchema()), builtin.Lift(builtin.TimeConvert()))
	register(toSkill(builtin.RegexTestSchema()), builtin.Lift(builtin.RegexTest()))
	register(toSkill(builtin.TextDiffSchema()), builtin.Lift(builtin.TextDiff()))
	register(toSkill(builtin.CsvQuerySchema()), builtin.Lift(builtin.CsvQuery(ws)))
	register(toSkill(builtin.UnitConvertSchema()), builtin.Lift(builtin.UnitConvert()))
	register(toSkill(builtin.HtmlToMarkdownSchema()), builtin.HtmlToMarkdownContext(t))

	register(toSkill(builtin.WikipediaSchema()), builtin.WikipediaContext(t))
	register(toSkill(builtin.CurrencyConvertSchema()), builtin.CurrencyConvertContext(t))
	register(toSkill(builtin.GithubSchema()), builtin.GithubContext(cfg.SkillGitHubToken, t))

	register(toSkill(builtin.FileListSchema()), builtin.Lift(builtin.FileList(ws)))
	register(toSkill(builtin.FileReadSchema()), builtin.Lift(builtin.FileRead(ws)))
	register(toSkill(builtin.FileWriteSchema()), builtin.Lift(builtin.FileWrite(ws)))
	register(toSkill(builtin.PdfTextSchema()), builtin.PdfTextContext(ws, t))

	// Monitoring
	state := builtin.StateDB{Path: cfg.SkillStateDB}
	register(toSkill(builtin.UptimeCheckSchema()), builtin.UptimeCheckContext(state))
	register(toSkill(builtin.SslLabsGradeSchema()), builtin.SslLabsGradeContext(builtin.SslLabsConfig{Email: cfg.SkillSSLLabsEmail}, t))
	register(toSkill(builtin.DnsPropagationSchema()), builtin.DnsPropagationContext())

	// Productivity (bookmark is a native skill: it needs vector memory)
	calNamed, calOrder := builtin.ParseNamedURLs(cfg.SkillCalendars)
	register(toSkill(builtin.CalendarIcsSchema()), builtin.CalendarIcsContext(builtin.CalendarConfig{Named: calNamed, Order: calOrder, Workspace: ws}, t))
	register(toSkill(builtin.ImapSearchSchema()), builtin.ImapSearchContext(builtin.ImapConfig{
		Host: cfg.SkillIMAPHost, Port: cfg.SkillIMAPPort, User: cfg.SkillIMAPUser,
		Password: cfg.SkillIMAPPassword, InsecureSkipVerify: cfg.SkillIMAPInsecure,
	}))
	register(toSkill(builtin.TodoSchema()), builtin.TodoContext(state))

	// Data
	sqlNamed, sqlOrder := builtin.ParseNamedURLs(cfg.SkillSQLDatabases)
	register(toSkill(builtin.SqlQuerySchema()), builtin.SqlQueryContext(builtin.SqlConfig{Named: sqlNamed, Order: sqlOrder, Workspace: ws}))
	register(toSkill(builtin.ChartRenderSchema()), builtin.Lift(builtin.ChartRender()))
	register(toSkill(builtin.YamlQuerySchema()), builtin.Lift(builtin.YamlQuery(ws)))

	// Integrations
	promNamed, promOrder := builtin.ParseNamedURLs(cfg.SkillPrometheus)
	register(toSkill(builtin.PrometheusQuerySchema()), builtin.PrometheusQueryContext(builtin.PrometheusConfig{
		Named: promNamed, Order: promOrder, Token: cfg.SkillPrometheusTok,
	}, t))
	apiNamed, apiOrder := builtin.ParseNamedURLs(cfg.SkillOpenAPI)
	apiCfg := builtin.OpenAPIConfig{
		Specs: apiNamed, Order: apiOrder, Auth: cfg.SkillOpenAPIAuth, BaseURL: cfg.SkillOpenAPIBaseURL,
		Workspace: ws, Timeout: time.Duration(t * float64(time.Second)),
	}
	register(toSkill(builtin.OpenAPISchema()), builtin.OpenAPIContext(apiCfg))
	// GET is a read; any other method changes something and is gated.
	reg.argApproval["openapi"] = builtin.OpenAPINeedsApproval(apiCfg)
}

func skillCreatorSchema() Skill {
	return Skill{
		Name: "skill_creator",
		Description: "Create, list, show, or delete dynamic skills. Use action='create' " +
			"when a user asks for a capability that no existing skill covers.",
		Parameters: map[string]Param{
			"action":            {Type: "string", Description: "create | list | show | delete", Required: true},
			"name":              {Type: "string", Description: "Skill name (lowercase, underscores, 2-49 chars)", Required: false},
			"description":       {Type: "string", Description: "Short skill description", Required: false},
			"parameters_schema": {Type: "object", Description: `Parameter definitions, e.g. {"city":{"type":"string","required":true}}`, Required: false},
			"endpoint":          {Type: "string", Description: "HTTP endpoint (omit for prompt-only)", Required: false},
			"method":            {Type: "string", Description: "GET or POST (default GET)", Required: false},
			"instructions":      {Type: "string", Description: "Free-text body appended after frontmatter", Required: false},
			"steps": {
				Type: "array",
				Description: `Pipeline steps. Each step: HTTP (name+endpoint) or skill (name+skill+args). ` +
					`Use {{param}} and {{step_name.path}} for placeholders.`,
				Required: false,
			},
		},
	}
}

func invalidNameMsg(name string) string {
	return fmt.Sprintf("invalid skill name %q (lowercase + underscores, 2-49 chars, start with letter)", name)
}

// safeNameChars is what keeps a skill name usable as a bare filename inside
// SKILLS_DIR. Every action that turns a name into a path must apply it: show
// and delete used to join the raw argument, so an LLM (or a playground caller)
// asking for "../../../etc/cron.d/x" read — or removed — files outside the
// skills directory.
var safeNameChars = func(name string) bool {
	if len(name) < 2 || len(name) > 49 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !isAlpha(r) {
				return false
			}
		} else {
			if !isAlpha(r) && !isDigit(r) && r != '_' {
				return false
			}
		}
	}
	return true
}

func skillCreatorFunc(skillsDir string, reg *Registry) func(map[string]any) (any, error) {
	return func(args map[string]any) (any, error) {
		action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
		switch action {
		case "create":
			return scCreate(skillsDir, reg, args)
		case "list":
			return scList(skillsDir)
		case "show":
			return scShow(skillsDir, args)
		case "delete":
			return scDelete(skillsDir, reg, args)
		default:
			return map[string]any{"error": fmt.Sprintf("unknown action %q (use create/list/show/delete)", action)}, nil
		}
	}
}

func scCreate(dir string, reg *Registry, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for create"}, nil
	}
	if !safeNameChars(name) {
		return map[string]any{"error": invalidNameMsg(name)}, nil
	}
	if reg.isReserved(name) {
		return map[string]any{"error": fmt.Sprintf("%q is a built-in skill and cannot be created or overwritten", name)}, nil
	}
	desc := strings.TrimSpace(strArg(args, "description"))
	if desc == "" {
		return map[string]any{"error": "description is required for create"}, nil
	}

	paramsRaw := args["parameters_schema"]
	if paramsRaw == nil {
		paramsRaw = args["parameters"]
	}
	paramsMap := normalizeParamsSchema(paramsRaw)

	endpoint := strings.TrimSpace(strArg(args, "endpoint"))
	method := strings.ToUpper(strings.TrimSpace(strArg(args, "method")))
	if method == "" {
		method = "GET"
	}
	instructions := strings.TrimSpace(strArg(args, "instructions"))
	steps := normalizeSteps(args["steps"])
	// A step calling the skill being defined recurses on every run. Deeper
	// cycles (a -> b -> a) are cut off at run time by the depth limit.
	for _, st := range steps {
		if callee, _ := st["skill"].(string); strings.EqualFold(strings.TrimSpace(callee), name) {
			return map[string]any{"error": fmt.Sprintf("pipeline step calls %q itself; recursive pipelines are not allowed", name)}, nil
		}
	}

	// build YAML frontmatter
	fm := map[string]any{"name": name, "description": desc}
	if len(paramsMap) > 0 {
		fm["parameters"] = paramsMap
	}
	if len(steps) > 0 {
		fm["steps"] = steps
	} else if endpoint != "" {
		fm["endpoint"] = endpoint
		fm["method"] = method
	}

	content, err := renderMarkdown(fm, instructions)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("render skill: %v", err)}, nil
	}
	fpath, created, err := writeSkillFile(dir, name, content)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	overwrite := !created
	s, err := markdown.Load(fpath)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("reload skill: %v", err)}, nil
	}

	// hot-register
	reg.registerMarkdown(s)

	skillType := "prompt-only"
	if len(steps) > 0 {
		skillType = "pipeline"
	} else if endpoint != "" {
		skillType = "endpoint"
	}
	status := "created"
	if overwrite {
		status = "updated"
	}
	var paramKeys []string
	for k := range paramsMap {
		paramKeys = append(paramKeys, k)
	}
	return map[string]any{
		"status":       status,
		"skill_name":   name,
		"file":         fpath,
		"description":  desc,
		"type":         skillType,
		"steps_count":  len(steps),
		"has_endpoint": endpoint != "",
		"parameters":   paramKeys,
		"hint":         fmt.Sprintf("Skill '%s' is now registered and can be called immediately.", name),
	}, nil
}

func scList(dir string) (any, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]any{"dynamic_skills": []any{}, "count": 0}, nil
	}
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	var out []map[string]any
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := markdown.Load(filepath.Join(dir, e.Name()))
		if err != nil {
			out = append(out, map[string]any{"file": e.Name(), "error": "parse error"})
			continue
		}
		out = append(out, map[string]any{
			"name":        s.Schema.Name,
			"description": s.Schema.Description,
			"file":        e.Name(),
		})
	}
	return map[string]any{"dynamic_skills": out, "count": len(out)}, nil
}

func scShow(dir string, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for show"}, nil
	}
	if !safeNameChars(name) {
		return map[string]any{"error": invalidNameMsg(name)}, nil
	}
	fpath := filepath.Join(dir, name+".md")
	raw, err := os.ReadFile(fpath)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("skill file not found: %s.md", name)}, nil
	}
	return map[string]any{"name": name, "file": fpath, "content": string(raw)}, nil
}

func scDelete(dir string, reg *Registry, args map[string]any) (any, error) {
	name := strings.ToLower(strings.TrimSpace(strArg(args, "name")))
	if name == "" {
		return map[string]any{"error": "name is required for delete"}, nil
	}
	if !safeNameChars(name) {
		return map[string]any{"error": invalidNameMsg(name)}, nil
	}
	fpath := filepath.Join(dir, name+".md")
	// A hand-written skill file may declare a frontmatter name that differs from
	// its filename; that declared name is what the registry is keyed by, so
	// resolve it before unlinking or the skill stays callable after deletion.
	registered := name
	if s, err := markdown.Load(fpath); err == nil && s.Schema.Name != "" {
		registered = s.Schema.Name
	}
	if err := os.Remove(fpath); err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"error": fmt.Sprintf("skill file not found: %s.md", name)}, nil
		}
		return map[string]any{"error": err.Error()}, nil
	}
	// Removing a stray file must never take a built-in down with it: a file
	// named after (or declaring) a built-in was never registered over it.
	if !reg.isReserved(registered) {
		reg.unregister(registered)
	}
	if registered != name && !reg.isReserved(name) {
		reg.unregister(name)
	}
	return map[string]any{"status": "deleted", "skill_name": registered, "file": fpath}, nil
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func isAlpha(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalizeParamsSchema(raw any) map[string]any {
	switch v := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for pk, pv := range v {
			switch x := pv.(type) {
			case string:
				out[pk] = map[string]any{"type": x, "description": pk, "required": false}
			case map[string]any:
				out[pk] = x
			default:
				out[pk] = map[string]any{"type": "string", "description": fmt.Sprintf("%v", x), "required": false}
			}
		}
		return out
	default:
		return nil
	}
}

func normalizeSteps(raw any) []map[string]any {
	switch v := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return v
	default:
		return nil
	}
}

// renderMarkdown builds the skill file. The frontmatter goes through yaml.v3
// rather than hand-written YAML: every key and value here comes from the
// model, and the hand-rolled writer emitted map keys raw, so a parameter
// named "x:\nendpoint: http://127.0.0.1:9/\nzz" injected top-level
// endpoint/steps fields (turning a "prompt-only" skill into an HTTP or
// self-calling pipeline skill), and a multi-line value was folded or ended
// the header at an embedded "---". The encoder quotes keys, block-indents
// multi-line strings and keeps typed values typed. Top-level fields keep a
// readable fixed order; nested maps are key-sorted.
func renderMarkdown(fm map[string]any, instructions string) (string, error) {
	order := []string{"name", "description", "parameters", "endpoint", "method", "steps"}
	for k := range fm {
		if !slices.Contains(order, k) {
			order = append(order, k)
		}
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range order {
		v, ok := fm[k]
		if !ok {
			continue
		}
		val := &yaml.Node{}
		if err := val.Encode(v); err != nil {
			return "", fmt.Errorf("encode %s: %w", k, err)
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, val)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	sb.WriteString("---\n")
	if instructions != "" {
		sb.WriteString("\n")
		sb.WriteString(instructions)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}
