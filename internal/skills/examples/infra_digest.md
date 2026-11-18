---
name: infra_digest
description: Daily infrastructure digest - new items from status/release RSS feeds in the last 24h plus an uptime check of your main site; made for scheduled agent tasks
parameters:
  feeds:
    type: string
    description: Comma-separated RSS/Atom feed URLs (status pages, release notes, security advisories)
    required: true
  url:
    type: string
    description: Main site URL to check, e.g. https://example.com
    required: true
steps:
  - name: news
    skill: rss_fetch
    args:
      urls: "{{feeds}}"
      since: 24h
      limit: 25
      summary_len: 200
  - name: site
    skill: uptime_check
    args:
      url: "{{url}}"
---
Write a short digest titled "Infra digest". First the site status (up/down,
24h uptime, latency). Then the feed items grouped by feed, one line each:
title and link; put anything mentioning an incident, outage, CVE or breaking
change first under "Needs attention". If nothing is new, say so in one line.
If this ran from a scheduled task or the user asked for delivery, send the
digest with notify (Telegram by default) and then answer with what was sent.
