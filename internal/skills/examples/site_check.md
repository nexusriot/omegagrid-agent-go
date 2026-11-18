---
name: site_check
description: Quick health card for a website - records an uptime check, breaks down request timing and checks the TLS certificate
parameters:
  url:
    type: string
    description: Full URL to check, e.g. https://example.com/health
    required: true
  host:
    type: string
    description: Hostname of that URL for the TLS check, e.g. example.com
    required: true
steps:
  - name: uptime
    skill: uptime_check
    args:
      url: "{{url}}"
  - name: timing
    skill: http_timing
    args:
      url: "{{url}}"
      samples: 3
  - name: tls
    skill: tls_probe
    args:
      host: "{{host}}"
---
Report in a few lines: up or down (and since when), uptime % for 24h/7d,
where the time goes (DNS / connect / TLS / first byte), and certificate
days left. Flag: down, uptime below 99%, first byte over 1 s, or a
certificate expiring within 21 days. If uptime shows changed=true, lead
with that.
