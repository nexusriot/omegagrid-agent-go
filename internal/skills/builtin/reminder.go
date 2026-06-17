package builtin

// The reminder skill has no external data source: it simply echoes its
// message back.  Its purpose is to give scheduled one-shot tasks something
// to "run" for plain reminders ("tell me at 3pm") — the scheduler runner
// delivers the echoed message via the task's Telegram notification.
func ReminderSchema() Skill {
	return Skill{Name: "reminder",
		Description: "Deliver a reminder message. Intended for scheduled tasks " +
			"(schedule_task with one_shot=true) so the message is pushed to the user " +
			"at the scheduled time. Returns the message as-is.",
		Parameters: map[string]Param{
			"message": {Type: "string", Description: "The reminder text to deliver", Required: true},
		}}
}

func Reminder() Executor {
	return func(args map[string]any) (any, error) {
		msg := str(args, "message")
		if msg == "" {
			return map[string]any{"error": "message is required"}, nil
		}
		return map[string]any{"reminder": msg}, nil
	}
}
