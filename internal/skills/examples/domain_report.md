---
name: domain_report
description: Full report on a domain - registration (RDAP), DNS A/MX, e-mail authentication (SPF/DMARC), TLS certificate and HTTP security headers
parameters:
  domain:
    type: string
    description: Domain to report on, e.g. example.com
    required: true
steps:
  - name: registration
    skill: rdap_lookup
    args:
      query: "{{domain}}"
  - name: dns_a
    skill: dns_lookup
    args:
      domain: "{{domain}}"
      record_type: A
  - name: dns_mx
    skill: dns_lookup
    args:
      domain: "{{domain}}"
      record_type: MX
  - name: mail_auth
    skill: email_auth
    args:
      domain: "{{domain}}"
  - name: tls
    skill: tls_probe
    args:
      host: "{{domain}}"
  - name: headers
    skill: http_headers
    args:
      url: "https://{{domain}}"
---
Write a concise domain report with these sections: Registration (registrar,
created/expiry dates, days until expiry), DNS (A and MX records), E-mail
(SPF and DMARC present and their policy), TLS (issuer, expiry, days left,
protocol), Web security headers (which important ones are missing).
End with a short "Action items" list for anything expiring within 30 days,
missing DMARC/SPF, or missing security headers. If a step failed, say so.
