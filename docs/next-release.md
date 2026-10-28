# System information, renewal sorting, and operations shortcuts

Upgrade **Server first, then Agents**. Back up the Server database before upgrading.

- Node Detail now shows the CPU model, operating system/version, and architecture.
  The Agent reads these once at startup and includes them in full reports. Upgrade
  both Server and Agent to populate the fields; no YAML changes or Token
  registration are required. Older Agents remain compatible and do not erase
  saved metadata. Cards retain their existing core/load line and height.
- The first header control toggles sorting by the next renewal date, soonest
  first. Click again to restore manual order. Permanent or undated nodes appear
  last; region filters, card/list views, and live updates keep working. The browser
  remembers the choice. Sorting does not change stored dates, node positions, or
  monthly traffic boundaries and requires only a Server update.
- The sort, view, and theme icons have transparent backgrounds with the same
  muted style as the GitHub icon. Active sorting uses a stronger icon color.
- README includes copyable Linux systemd and OpenWrt / ImmortalWrt shortcuts that
  download the installer, run `upgrade --keep-config`, and restart the Agent.
  Each step requires the previous step to succeed. YAML and Token are retained;
  optional new settings are not inserted into existing configuration.
- Runtime logs rate-limit repeated failures and emit recovery messages through
  the existing system logger. The supplied Docker Compose file rotates container
  logs at 10 MB per file, retaining three files. Existing deployments must add the
  logging block to their local Compose file and recreate the container to enable
  rotation; an image upgrade alone does not apply it.
- Fresh Linux Agent installs no longer print a misleading missing-unit diagnostic
  while probing the previous service state. Actual deployment failures remain
  visible. README operations, paths, settings, and full Token lookup are clarified.

## Upgrade

Use the **Deployment & Operations** section in README. Upgrade the Server before
running the Agent shortcuts. `--keep-config` preserves billing, renewal dates,
interfaces, TCP Ping targets, allowances, intervals, and Token without prompts.
The shortcut explicitly restarts the Agent, including a previously stopped
service, while keeping its boot autostart setting. No traffic accounting or
retention algorithms change in this release.

## Validation

Local checks cover Go tests with the race detector, static analysis, installer
regressions, dashboard regressions, and Linux Agent cross-builds. Real Linux
systemd lifecycle checks, Docker isolation, and execution of both Linux release
archives remain required CI gates before Release artifacts are published.
