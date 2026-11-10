# v0.7.18 — OpenWrt installer safety and cleanup

- Reject OpenWrt / ImmortalWrt configuration directories not owned by root or
  writable by group or others before installation, upgrades, removal or status.
- Safely stage and atomically replace the installed version file so symbolic
  links and hardlinks cannot redirect version writes into other router files.
- Include version staging in preflight checks, rollback and uninstall cleanup;
  preserve existing configuration, Tokens and service state on failure.
- Reuse one YAML quoting function in the OpenWrt installer and remove the old
  Makefile packaging target; release archives use the verified Release workflow.
- Document OpenWrt directory and file permissions in README.

Download the current OpenWrt installer to apply these safeguards. No Agent YAML
or Token changes are required. Monitoring and traffic accounting are unchanged.
