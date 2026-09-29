# Security

Please use **Report a vulnerability** on the repository's [Security tab](https://github.com/themartz90/folderhug/security) for private reports. Do not post source credentials, API tokens, cookies, private URLs, downloaded files, or runtime databases in a public issue.

Include the affected revision, deployment configuration with secrets removed, expected behavior, actual behavior, and a small synthetic reproduction.

## Deployment boundaries

The app uses one owner account. API tokens are restricted by profile and scope. Source credentials stay on the server: temporary credentials stay in memory, while remembered credentials are encrypted with a key outside the database. Protect that key, the database, downloads, and backups with host-level access controls.

The supplied Compose file exposes the app only on loopback. Complete first-run owner setup there before allowing remote access; the first visitor to a fresh installation can create the owner account. Set the exact public origin and use HTTPS at a reverse proxy for remote access. Do not expose a development server. The app checks Host, Origin, and CSRF tokens; forwarded headers do not override those checks. Plain HTTP upstream access requires explicit opt-in and offers no transport confidentiality.

All source requests pass through origin, path, DNS/IP, redirect, timeout, and byte-limit checks. Operator-controlled private-network allowances should be narrow. The optional video worker has a separate key and bounded broker access; it receives no upstream credentials, database, credential key, downloads mount, or general network access in the supplied Compose configuration.

These controls do not replace host security or dependency maintenance. Keep the host, container runtime, reverse proxy, and selected dependency builds updated. Review the exact source and configuration you deploy. No independent security audit or security guarantee is claimed.

**Sign out all devices** revokes owner browser sessions but leaves API tokens active. Revoke an exposed API token in Settings. If the owner password is exposed, use owner recovery to set a new password and revoke all browser sessions and API tokens. If remembered source credentials may be exposed, rotate them at the upstream service as well. Restoring a backup restores its saved authentication records, so account for that during recovery. See [docs/OPERATIONS.md](docs/OPERATIONS.md).
