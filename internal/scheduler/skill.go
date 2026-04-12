package scheduler

import (
	"fmt"
	"strings"
)

// ScheduleTaskSkill is the Go-side replacement for the Python schedule_task
// skill.  It is registered directly into the agent's tool table by the gateway
// so that the agent can create / list / delete cron tasks against the Go
// scheduler store.
type ScheduleTaskSkill struct {
	Store *Store
}

// SkillSchema returns the JSON schema entry the agent loop merges with the
// sidecar's skill list when assembling the system prompt.
func (s *ScheduleTaskSkill) SkillSchema() map[string]any {
	return map[string]any{
		"name": "schedule_task",
		"description": "Manage scheduled tasks. Actions: 'create' a new recurring task " +
			"(runs a skill on cron schedule, optionally notifies Telegram), 'list' all scheduled tasks, " +
			"'delete' a task by id, 'enable'/'disable' a task by id.",
		"parameters": map[string]any{
			"action":                  map[string]any{"type": "string", "description": "Action: create, list, delete, enable, disable", "required": true},
			"name":                    map[string]any{"type": "string", "description": "Task name (for create)", "required": false},
			"cron_expr":               map[string]any{"type": "string", "description": "Cron expression, e.g. '*/5 * * * *' (for create)", "required": false},
			"skill":                   map[string]any{"type": "string", "description": "Skill to run, e.g. 'ping_check', 'weather' (for create)", "required": false},
			"args":                    map[string]any{"type": "object", "description": "Arguments for the skill (for create)", "required": false},
			"notify_telegram_chat_id": map[string]any{"type": "number", "description": "Telegram chat ID to send results to (for create). Use the current chat_id if user asks for Telegram notifications.", "required": false},
			"task_id":                 map[string]any{"type": "number", "description": "Task ID (for delete/enable/disable)", "required": false},
		},
	}
}

// Execute implements the agent's skill calling convention.  args mirrors what
// the LLM emits, so we accept loose types and coerce.
func (s *ScheduleTaskSkill) Execute(args map[string]any) any {
	action := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	switch action {
	case "create":
		return s.create(args)
	case "list":
		return s.list()
	case "delete":
		return s.delete(args)
	case "enable":
		return s.setEnabled(args, true)
	case "disable":
		return s.setEnabled(args, false)
	default:
		return map[string]any{"error": fmt.Sprintf("Unknown action: %s. Use: create, list, delete, enable, disable", action)}
	}
}

func (s *ScheduleTaskSkill) create(args map[string]any) any {
	cron := asString(args["cron_expr"])
	if cron == "" {
		return map[string]any{"error": "cron_expr is required (e.g. '*/5 * * * *')"}
	}
	skill := asString(args["skill"])
	if skill == "" {
		return map[string]any{"error": "skill is required (e.g. 'ping_check', 'weather')"}
	}
	name := asString(args["name"])
	if name == "" {
		name = fmt.Sprintf("%s (%s)", skill, cron)
	}

	innerArgs, _ := args["args"].(map[string]any)
	if innerArgs == nil {
		innerArgs = map[string]any{}
	}

	var notifyChat *int64
	if v, ok := args["notify_telegram_chat_id"]; ok && v != nil {
		if n := asInt64(v); n != 0 {
			notifyChat = &n
		}
	}

	task, err := s.Store.Create(name, cron, skill, innerArgs, notifyChat)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"created": true, "task": task}
}

func (s *ScheduleTaskSkill) list() any {
	tasks, err := s.Store.ListAll()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"count": len(tasks), "tasks": tasks}
}

func (s *ScheduleTaskSkill) delete(args map[string]any) any {
	id := asInt64(args["task_id"])
	if id == 0 {
		return map[string]any{"error": "task_id is required"}
	}
	ok, err := s.Store.Delete(id)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Task %d not found", id)}
	}
	return map[string]any{"deleted": true, "task_id": id}
}

func (s *ScheduleTaskSkill) setEnabled(args map[string]any, enabled bool) any {
	id := asInt64(args["task_id"])
	if id == 0 {
		return map[string]any{"error": "task_id is required"}
	}
	ok, err := s.Store.SetEnabled(id, enabled)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if !ok {
		return map[string]any{"error": fmt.Sprintf("Task %d not found", id)}
	}
	return map[string]any{"ok": true, "task_id": id, "enabled": enabled}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}
