---
title: Deployment
nav_order: 6
has_children: true
---

# Deployment

my-sideload-library can run in Docker, directly on macOS or Windows, or from a Debian package on 64-bit x86 and ARM Linux. Choose a platform from the navigation to get started.

## Persistent data

Keep `DATA_DIR` on persistent storage. It contains the SQLite index, user database, cover cache, and application settings. The source EPUB directories can remain read-only.

## Public URLs and email

Set `PUBLIC_URL` to the externally reachable HTTPS URL when password-reset or invitation email is enabled. This prevents emailed links from being constructed from a client-controlled request host.

## HTTPS

Serve the application through an HTTPS reverse proxy. The application listens on HTTP at `PORT` for the proxy, but all session cookies are marked `Secure` and browsers will only send them to the HTTPS public endpoint.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LIBRARY_PATH` | `/library` | Comma-separated EPUB directories |
| `DATA_DIR` | `/data` | Writable application data |
| `SESSION_SECRET` | Empty | Runtime session-cookie signing-key override |
| `PORT` | `8080` | HTTP listen port |

Configure the site name, public URL, cover width, page size, session lifetime, and enhancement providers from the administrator interface after setup. Those settings are persisted in `DATA_DIR/users.db`.

For the full development environment and contribution terms, see [CONTRIBUTING.md](https://github.com/sethwv/my-sideload-library/blob/main/CONTRIBUTING.md).

On first startup, the application displays a one-time setup page for creating the first administrator account, then signs that account in. It also generates and persists a session signing secret in `DATA_DIR/users.db`. Set `SESSION_SECRET` only when an operational override is needed. The override is not stored, and changing it invalidates existing sessions.

If an administrator password is lost, stop the application and run `sideload-library admin reset-password <username> <password>` with the same `DATA_DIR`. It sets the supplied password without starting the server. The same local break-glass CLI can create an account when necessary: `sideload-library admin create-user <username> <password> [role]`.
