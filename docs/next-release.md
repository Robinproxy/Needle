# Public dashboard with administrator-managed node profiles

The dashboard remains public. An optional single-administrator `/admin` page now
edits display name, region, position, currency, billing amount/period, renewal
anchor, and monthly upload + download allowance. No remote shell, Agent service
management, Token management, or node deletion is added to the web interface.

## Upgrade

Back up the complete Server database directory and upgrade **Server first**.
Existing Agent binaries, YAML, Tokens, and history remain compatible; existing
Agents do not need upgrading for the management features.

Initialize the administrator once, interactively on the Server host:

```sh
# Docker: run from the Server Compose directory
docker compose exec needle-server needle-server -db /data/needle.db admin-init

# Binary: use the actual database path if customized
sudo /opt/needle/bin/needle-server -db /opt/needle/data/needle.db admin-init
```

Choose the command matching your deployment. Enter a username, the public HTTPS
origin without a path, and a password of at least 15 characters twice. Password
entry is hidden before the prompt appears. `admin-reset` changes credentials or
the public origin and invalidates old sessions without restarting the Server.
Subsequent upgrades retain the administrator. No default account or public
initialization endpoint exists; the public dashboard works before setup.

## Profile ownership and traffic continuity

- Existing node values automatically populate the editor. Until the first admin
  save, legacy nodes continue using Agent-provided settings. Saving transfers
  that static profile to the Server; later Agent reports cannot overwrite it.
- Display names are separate from the stable Agent hostname and Token binding.
- On takeover, the existing monthly traffic reset day is pinned independently of
  renewal dates. It is read-only in this version. Editing costs, renewal dates,
  periods, or allowances preserves the current boundary and accumulated usage.
  Legacy nodes not yet taken over retain their date-derived reset behavior.
- Fresh Agent installations now configure connection and collection settings
  only and set `server_managed: true`. Complete static settings in `/admin`.
  Unconfigured billing/dates show Not set. Fresh nodes use monthly reset day 1
  and an unrestricted allowance; finite allowances and the infinity option are
  available in the editor. No automatic historical traffic correction occurs.
- Old YAML is retained on upgrades. Reselection skips legacy billing prompts for
  `server_managed: true` configurations. New installers require the compatible
  `server-profiles-v1` Agent marker and reject older release binaries before
  changing an installation; use the installer bundled with an older release.

## Security and interface

Administrator credentials are independent of Agent Tokens. Passwords use salted
Argon2id hashes in SQLite. Sessions are bounded, memory-only, expire after eight
hours, and end on logout or Server restart. Management writes require an admin
session, matching origin, JSON content, and CSRF verification. Login attempts,
password verification concurrency, and request sizes are bounded.

Profile saves are transactional and reject stale forms; the most recent 200
changes are recorded in SQLite. Public cards update after saves. Node detail
expansion no longer automatically scrolls the page; keyboard focus restoration
also preserves the viewport. README covers setup, recovery, and legacy settings.

## Validation

Local Go/race tests, vet, installer/dashboard regressions, native terminal
initialization/reset/cancellation, and Linux amd64/arm64 Agent cross-builds pass.
CI additionally verifies root CLI terminal setup after switching to the database
owner, credential reset revocation, real systemd lifecycle, Docker permissions,
and execution/checksums of both architectures' packaged release archives before
publishing Release assets.
