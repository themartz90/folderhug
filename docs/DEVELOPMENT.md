# Windows development

Use Go 1.27.2, Node 24, and npm 11.5.1 or newer within major version 11. The repository pins Node 24.21.0 in `.node-version`; `web/package.json` records npm 11.19.0. The examples below use PowerShell from the repository root. Docker and WSL are not required for native app development.

Native development uses port `8080`. Docker Compose publishes host port `48321` by default.

## Local testing without a password

After building the frontend, run `go run ./cmd/folderhug dev` from the repository root and open `http://localhost:8080`. This command creates its own state in `.local/dev` and signs you in automatically. No setup command or password entry is needed. It accepts only loopback connections and a localhost or loopback public URL. Keep it local; do not expose it through a proxy or tunnel.

Development mode keeps profiles and downloads between restarts. It generates an owner secret and credential key inside `.local/dev/keys`; keep those with the development database. Existing normal app data is separate. Restart the command after signing out or when the 30-day session expires.

For frontend hot reload, use the two-terminal workflow below, replacing `go run ./cmd/folderhug` with `go run ./cmd/folderhug dev` in the backend terminal.

## Build and run

```powershell
cd web
npm ci
npm run build
cd ..
go run ./cmd/folderhug
```

Open `http://localhost:8080` and create the owner password in the browser. Use at least 6 Unicode characters, with no required character types and a maximum of 1,024 UTF-8 bytes. By default the app accepts localhost and literal IP addresses at its listening port; if you set `FOLDERHUG_PUBLIC_URL`, the browser must use that exact origin.

The app creates `.local/keys/credentials.key` on a fresh installation. The CLI `setup` command is optional when you need to initialize the owner before opening the browser. The first visitor to a fresh installation can claim the owner account, so complete setup before exposing the address. Do not run two app instances against the same state directory. Stop the app with Ctrl+C before password recovery or copying its state.

Build a native executable after the frontend:

```powershell
go build -o bin/folderhug.exe ./cmd/folderhug
```

## Frontend development server

Stop the app before changing its public origin. In the backend terminal:

```powershell
$env:FOLDERHUG_PUBLIC_URL = 'http://127.0.0.1:5173'
go run ./cmd/folderhug
```

In a second terminal:

```powershell
cd web
npm run dev
```

Open `http://127.0.0.1:5173`. Vite proxies `/api` and `/health` to `127.0.0.1:8080`, preserving the browser's host. The configured public origin must match the Vite URL for login and CSRF checks. The backend still listens on port 8080. Use the built app, rather than the development server, outside a local development machine.

To return to the built app, stop both processes and clear this terminal override before restarting Go:

```powershell
Remove-Item Env:FOLDERHUG_PUBLIC_URL
```

## Checks

The repository check script installs locked npm dependencies, checks TypeScript and generated API types, runs lint and unit tests, builds the frontend, then runs Go tests, vet, and a native build:

```powershell
./scripts/check.ps1
```

On Linux, the equivalent is `sh scripts/check.sh`. Browser tests are a separate step:

```powershell
cd web
npx playwright install chromium firefox webkit
npm run test:e2e
```

The browser harness builds the production frontend and a Go binary with the `webfixture` tag. It uses an isolated synthetic source and temporary runtime directory at `127.0.0.1:4173`, then removes its state on shutdown. Leave that port free. Playwright reports and traces are ignored by Git; review them before sharing because an independently run browser session may contain private data.

## API and persistence changes

Edit `api/openapi.yaml`, then regenerate and check the TypeScript contract:

```powershell
cd web
npm run api:generate
npm run api:check
```

Applied SQL migrations are immutable. Add a new migration and test both a clean database and an upgrade containing synthetic data. Do not use an active database as a test fixture.

Native checks do not substitute for Linux race and container checks. The CI definition includes `go test -race ./...`, three browser engines, Compose validation, and a container build. Run those checks in CI for changes that affect their behavior.

Dependency checks used by CI can also run locally:

```powershell
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
cd web
npm audit
```

Read the reported reachability as well as the advisory count. A module can contain an affected package that the app does not import.

Resource checks are opt-in on Linux and use synthetic data:

```sh
FOLDERHUG_RESOURCE_ACCEPTANCE=1 GOMEMLIMIT=384MiB \
go test -p 1 -count=1 -timeout=3m -run '^TestResource' -v \
./internal/scans ./internal/content ./internal/catalog
```

Run them without the race detector. They measure the test process through procfs, including a 100,000-entry index, a 1 GiB loopback stream, and cached listing latency. These are test harness measurements, not measurements of a deployed app.
