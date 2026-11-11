# v0.7.19 — Keep credential management on the Server

- Remove Agent self-revocation and the public `/api/unregister` endpoint.
  Manage credentials with the Server administrator's `revoke-token` command;
  `delete-agent` remains the separate operation for deleting node data.
- Remove remote revocation from Linux and OpenWrt / ImmortalWrt installers.
  Uninstall only cleans local services, files and service identities, and no
  longer depends on Server connectivity.
- Retain `/api/report-capabilities` for historical report deduplication and
  retry negotiation; remove only its self-revocation capability field.
- Update uninstall commands and credential management guidance in README.

Upgrade the Server to disable the endpoint for existing Agents as well. Download
the current installer for local-only removal. Agent upgrades can keep their YAML
and Token; collection, reporting and traffic accounting are unchanged.
