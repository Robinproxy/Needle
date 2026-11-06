# v0.7.14 — TCP Ping name validation

- Share the Server's existing TCP Ping name normalization with Agent validation.
  Names that differ only by surrounding Unicode whitespace now fail the Agent's
  duplicate-name check before collection or reporting starts.
- Reject invalid UTF-8, control characters, blank names, and names over the
  existing 64-byte Agent limit with explicit configuration errors.
- Trim ASCII padding and reject invalid names in both Linux and OpenWrt
  installers. Candidate configuration validation runs before replacing files;
  failed upgrades preserve the existing installation, configuration, and Token.
- Preserve stored labels, original report payloads, persistent receipt digests,
  and existing Server normalization behavior.
- Add Agent/Server contract, retry, installer, and UTF-8 boundary regressions;
  clarify the naming rules in README.

Upgrade the Agent to apply the validation fix. Normal configurations can be
kept unchanged; rename colliding or invalid probe names before retrying an
upgrade that fails configuration validation. No Server upgrade is required.
