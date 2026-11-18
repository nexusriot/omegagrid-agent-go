---
name: dns_cutover_check
description: Verify a DNS migration - how far a new A record has propagated across public resolvers, and whether the site answers with a valid certificate
parameters:
  name:
    type: string
    description: Hostname being moved, e.g. www.example.com
    required: true
  expected:
    type: string
    description: The new IP address the record should resolve to
    required: true
steps:
  - name: propagation
    skill: dns_propagation
    args:
      name: "{{name}}"
      expected: "{{expected}}"
  - name: tls
    skill: tls_probe
    args:
      host: "{{name}}"
  - name: health
    skill: http_health
    args:
      url: "https://{{name}}"
---
Answer: what share of resolvers already return the new address (list the
ones that do not, with what they return), whether the site responds, and
whether the certificate is valid for the name and not expiring soon. Finish
with a verdict: "cutover complete", "propagating" (suggest checking again
later, or offer to schedule a re-check with schedule_task) or "problem".
