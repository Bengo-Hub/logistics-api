# Security Policy

Logistics data is critical to Codevertex operations. Follow these guidelines to keep it secure.

## Supported Versions

| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| Latest tagged release (future) | ✅ |
| Older branches | ❌ |

## Reporting Security Issues

1. Email `security@bengobox.com` with the vulnerability details (do not open a public issue).
2. Provide reproduction steps, impact, and recommended mitigations if possible.
3. You will receive acknowledgement within 48 hours and follow-up until resolution.

## Secure Development Practices

- Protect routing and telemetry APIs with authentication/mTLS.
- Sanitize and validate all inbound payloads (especially telemetry and carrier webhooks).
- Avoid storing sensitive customer/location data longer than necessary; honour retention policies.
- Encrypt secrets and private keys, rotate regularly via KMS.
- Monitor audit logs and anomaly alerts for suspicious activity.

## Operational Hardening

- Enable TLS for all network paths (ingress, service-to-service).
- Use least-privilege access for databases and message brokers.
- Keep routing engine containers updated with latest patches.
- Run `govulncheck` and dependency scans as part of CI/CD.

Thank you for safeguarding the logistics platform.

