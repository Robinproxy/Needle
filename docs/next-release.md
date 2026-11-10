# v0.7.17 — Report admission and installer safety

- Authenticate and rate-limit realtime reports before reading their bodies;
  reject unauthenticated slow uploads promptly.
- Verify exclusive service UIDs and groups for Linux Server and Agent installs
  and upgrades, including primary group memberships and numeric aliases.
- Refuse Agent uninstall when a service group has no matching user or identity
  lookup fails, before stopping services, deleting files, or revoking Tokens.
- Quote Linux Agent registration commands and new YAML Tokens so shell and
  YAML special characters preserve their literal value.
- Use the digest returned by the current Docker push for image publication;
  retain serialized, current-main-only promotion of tested images to latest.
- Simplify README operations and replace specific node examples with placeholders.

Upgrade the Server to apply realtime admission protection. Download the current
Linux installers for install, upgrade, and removal safeguards. Existing Agent
configuration and Tokens remain unchanged with `upgrade --keep-config`.
