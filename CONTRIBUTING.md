# Contributing to my-sideload-library

By submitting a pull request, you confirm that you have the right to submit the contribution and license it under the GNU Affero General Public License v3.0 only.

If your employer or another party owns the contribution, obtain its authorization before submitting it.

Contributions must not include code, assets, or data whose license is incompatible with AGPL-3.0-only. Preserve all required third-party notices and identify their source and license in the pull request.

Submitting a pull request grants my-sideload-library and its maintainers a perpetual, worldwide, non-exclusive, royalty-free, irrevocable license to use, modify, distribute, sublicense, and relicense the contribution as part of my-sideload-library under any [OSI-approved open-source license](https://opensource.org/licenses). This permission does not transfer copyright ownership.

## Development Environment

Install Go 1.25.7 and Docker Compose. Clone the repository, then download dependencies:

```bash
cd src
go mod download
```

For a local server populated with the screenshot fixture books, run:

```bash
scripts/dev-server.sh
```

It generates the EPUBs and stores the catalog plus application data under the ignored `.dev/` directory. On first run, open the server and create an administrator through the one-time setup page. Set `MY_SIDELOAD_LIBRARY_FIXTURE_DIR` or `MY_SIDELOAD_LIBRARY_DATA_DIR` to use other locations, or set `PORT` to change the listen port.

With the VS Code Go extension installed, choose **Debug fixture server** from Run and Debug. Its pre-launch task performs the same fixture and account setup before starting the debugger.

To use your own EPUB directory instead:

```bash
LIBRARY_PATH=/path/to/epubs \
DATA_DIR=/tmp/my-sideload-library-data \
(cd src && go run ./cmd/server)
```

Keep `DATA_DIR` for account recovery. With the server stopped, the break-glass CLI can set an operator-supplied password with `admin reset-password <username> <password>` or create an account with `admin create-user <username> <password> [role]`.

For Docker development, add local credentials and the EPUB bind mount in `docker-compose.override.yml`, then run `docker compose up --build`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LIBRARY_PATH` | `/library` | Comma-separated read-only EPUB directories |
| `DATA_DIR` | `/data` | Writable SQLite indexes, user database, and cover cache |
| `SESSION_SECRET` | Empty | Runtime session-cookie signing-key override |
| `PORT` | `8080` | HTTP listen port |

`SESSION_SECRET` is generated and stored in `DATA_DIR/users.db` when unset. Setting it uses that value for the current process without changing the stored secret, so changing or removing an override invalidates active sessions. Configure site settings and integrations from the administrator interface; they are persisted in `DATA_DIR/users.db`.

## Validation

Run these before opening a pull request:

```bash
cd src
go test ./...
go vet ./...
go build ./...
```

UI changes should also be checked in a modern browser. Kobo's browser has older QtWebKit limitations, so confirm device behavior when changing CSS or JavaScript compatibility-sensitive code.
