---
name: morning_briefing
description: Morning briefing - today's calendar events (first configured calendar), due and overdue to-dos, and the weather
parameters:
  city:
    type: string
    description: City for the weather
    required: true
  timezone:
    type: string
    description: IANA time zone, e.g. Europe/Berlin
    required: true
steps:
  - name: calendar
    skill: calendar_ics
    args:
      from: today
      days: 1
      timezone: "{{timezone}}"
  - name: todos
    skill: todo
    args:
      action: list
      status: today
      timezone: "{{timezone}}"
  - name: weather
    skill: weather
    args:
      city: "{{city}}"
---
Write a friendly, compact morning briefing: the weather in one line, then
today's events in time order (time, title, location), then the to-dos due
today with overdue ones marked. If the calendar step errored because no
calendar is configured, skip that section and mention SKILL_CALENDARS once.
If this ran from a scheduled task, deliver it with notify.
