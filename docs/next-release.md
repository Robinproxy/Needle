# v0.7.13 — Agent redirect security

- Apply one redirect policy to realtime reports, historical reports, capability
  negotiation, and history-preserving unregister.
- Reject HTTPS-to-HTTP, cross-host, subdomain, port-changing, and embedded-
  credential redirects, even when plaintext HTTP is explicitly enabled.
- Reject redirects that change the request method and bound redirect chains
  to ten requests. Keep safe same-origin redirects and POST replay via 307/308.
- Handle rejected redirects through bounded status responses without logging
  the supplied redirect URL or response body; do not retry rejected redirects.
- Add security regressions, verified TLS redirect fixtures, and configuration
  and troubleshooting documentation.

Upgrade the Agent on Linux and OpenWrt/ImmortalWrt to apply this fix. The Server
requires no changes. Keep existing configuration and Tokens; if the configured
Server URL redirects to a different origin, set it to the final Server address.
