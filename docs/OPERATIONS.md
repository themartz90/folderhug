# Compose operations and backups

The core deployment uses one app container and SQLite. The default `compose.yaml` pulls the app image from GHCR. The optional video poster worker is described in [MEDIA_WORKER.md](MEDIA_WORKER.md). These examples use a Linux shell from a directory containing the Compose file.

## First start

```sh
docker compose pull
docker compose --profile tools run --rm setup
docker compose up -d app
docker compose ps
```

Setup prompts for an owner password of at least 12 characters. It refuses to replace an existing owner. Open `http://localhost:8080` on the host and sign in. The port binds to loopback by default. Check `http://localhost:8080/health/ready` for database readiness; it does not check upstream sources.

Keep the same Compose project name and file when updating an installation. Changing the project name selects different named volumes. Do not run multiple app instances against the same database. The default project name remains `http-wrapper` to preserve existing volumes. For a fresh installation, you can use `-p folderhug` on every Compose command. If you already started with `-p`, keep that same value.

## Build from source

Clone the repository to get the build context and use `compose.build.yaml` as an override. Use both files for every command in this installation:

```sh
git clone https://github.com/themartz90/folderhug.git
cd folderhug
docker compose -f compose.yaml -f compose.build.yaml build app setup
docker compose -f compose.yaml -f compose.build.yaml --profile tools run --rm setup
docker compose -f compose.yaml -f compose.build.yaml up -d app
```

Both modes use the same volumes when the Compose project name stays the same. Do not run two app containers against those volumes at once. Record the source revision or image digest before updating.

The remaining examples use the default image installation without overlays. If you use a mounted-folder override, include `-f compose.yaml -f compose.mounts.yaml` in every later Compose command so an update keeps the mount. For a source build, use `-f compose.yaml -f compose.build.yaml`, followed by any mount override. Keep any `-p` project name you selected at first start. To choose another local port, set both values before running Compose:

```sh
export FOLDERHUG_PORT=18080
export FOLDERHUG_PUBLIC_URL=http://localhost:18080
```

`/health/live` reports that the HTTP server is responding. `/health/ready` also checks the database. Neither checks upstream credentials or source availability. `docker compose logs --tail=100 app` shows recent app logs; inspect logs before sharing them.

## Configuration

The native app reads environment variables directly. It does not load a `.env` file. Compose performs its normal variable interpolation, but only variables declared in the Compose service reach the container. Use an override file with `services.app.environment` for additional settings.

Settings use the `FOLDERHUG_` prefix. The app and media worker still accept `HTTPWRAPPER_` settings when the matching new variable is absent. If both are present, `FOLDERHUG_` takes precedence. Update old environment keys in Compose overrides too, since the base file now supplies the new names. The Compose port, public URL, and mounted-root substitutions also accept their old names.

The session cookie is `folderhug_session`. The database filename and stored encryption identifiers retain their original names so upgrades can read existing data. Leave those files in place. The `http-wrapper` command remains an alias for `folderhug` in the container, and the old source entry point remains available.

| Variable | Native default | Purpose |
| --- | --- | --- |
| `FOLDERHUG_ADDR` | `127.0.0.1:8080` | Listening address; Compose sets `0.0.0.0:8080` inside its container |
| `FOLDERHUG_PUBLIC_URL` | `http://localhost:8080` | Exact public origin for Host, Origin, and cookie checks |
| `FOLDERHUG_DATA_DIR` | `.local/data` | SQLite database and related files |
| `FOLDERHUG_KEYS_DIR` | `.local/keys` | Credential encryption key |
| `FOLDERHUG_DOWNLOADS_DIR` | `.local/downloads` | Completed and partial server downloads |
| `FOLDERHUG_CACHE_DIR` | `.local/cache` | Disposable thumbnail cache |
| `FOLDERHUG_STREAM_MAX_BYTES` | `1099511627776` | Maximum bytes per proxied source stream |
| `FOLDERHUG_STREAM_TIMEOUT` | `24h` | Stream deadline, from `1s` to `168h` |
| `FOLDERHUG_DOWNLOAD_BUDGET_BYTES` | `107374182400` | Download storage budget, 100 GiB |
| `FOLDERHUG_DOWNLOAD_RESERVE_BYTES` | `5368709120` | Free-space reserve, 5 GiB; zero disables this check |
| `FOLDERHUG_DOWNLOAD_UNKNOWN_BYTES` | `21474836480` | Reservation for an unknown-length download, 20 GiB |
| `FOLDERHUG_THUMBNAIL_CACHE_BYTES` | `1073741824` | Thumbnail cache budget, 1 GiB |
| `FOLDERHUG_UPSTREAM_ALLOW_FILE` | unset | Operator-controlled private-network allow file |
| `FOLDERHUG_MEDIA_SOCKET` | unset | Optional worker broker socket |
| `FOLDERHUG_WORKER_KEY` | `.local/worker-key/worker.key` | Separate app-to-worker authentication key |

Compose mounts `/data`, `/keys`, `/downloads`, and `/cache` from named volumes. It mounts `/keys` read-only in the app. The setup service writes the key as UID/GID 10001. Keep that ownership when restoring files. A writable host folder used as a mounted download destination also needs access for UID/GID 10001; check its existing permissions before changing ownership or mode.

The public URL is an origin, with no path, query, fragment, or credentials. A reverse proxy must preserve the public Host and Origin headers and forward to the loopback app port. Configure HTTPS at that proxy, disable response buffering for streaming downloads, and allow long responses and server-sent events. Forwarded headers do not replace the app's configured origin. Deployment under a URL subpath is not supported.

[examples/nginx.conf](../examples/nginx.conf) is a complete example configuration for nginx on the same host as the loopback Compose port. Replace `files.example.test` and the certificate paths, provide a valid certificate and key, and set `FOLDERHUG_PUBLIC_URL=https://files.example.test` before starting the app. If nginx already serves other applications, adapt the server block into that installation rather than replacing its entire configuration. A proxy in another container needs a different upstream address: its own `127.0.0.1` is not the host's app port.

The example keeps Host unchanged and lets nginx forward Origin, authorization, cookies, and range headers. It uses HTTP/1.1, disables response buffering and caching, and allows a 24-hour gap between upstream reads for long downloads and event streams. These directives follow the [nginx proxy module documentation](https://nginx.org/en/docs/http/ngx_http_proxy_module.html).

Private upstream addresses are rejected by default. An operator may provide a JSON allow file with exact host, port, and CIDR rules, for example:

```json
[{"host":"nas.example.test","port":8080,"cidrs":["192.168.1.0/24"]}]
```

Mount that file read-only and set `FOLDERHUG_UPSTREAM_ALLOW_FILE` to its container path. The file is limited to 64 KiB and 128 rules. Rules do not permit loopback, link-local, or metadata destinations. Do not turn a broad network range into a substitute for choosing the intended source.

## Interrupted scans

An interrupted scan can resume within its remaining entry and active-time limits. If a newer scan overlaps the same folder, start a new scan instead. The app keeps the newer index observations and reports `scan_superseded` for the older continuation.

Scan phases reserve at most 30 seconds before fetching, saving a directory, or pruning the index. Normal completion records the actual duration. After an abrupt stop, the outstanding reservation remains charged, so a crash can consume up to 30 seconds of unused budget. Time spent offline is excluded. Repeated crashes can exhaust a job's budget; start a new scan when that happens. A phase that cannot finish within 30 seconds stops without claiming complete coverage.

Stop the app gracefully before upgrading. If a scan was interrupted on an older build that did not reserve unfinished scan time, start a fresh scan for that scope after upgrading.

## Back up a stopped installation

There is no built-in backup command. Stop the app before copying state. Copying only a live SQLite database file can omit committed data in its WAL file. For an installation with the media overlay, stop the worker as well using the same Compose files and project name used at startup.

Preserve these items together:

- The entire data directory, including any SQLite WAL and SHM files.
- The original `credentials.key`, stored in a separately protected backup location.
- The downloads directory, including partial files, so job records and files agree.
- The configuration, source revision, and image identity used by that installation.
- The worker key when retaining an existing media-worker setup. Both app and worker must use the same key.

The thumbnail cache can be recreated. Treat the database and download archives as private: they contain source paths, job history, token records, and downloaded files. Keep the key backup separate from the database backup when your backup system supports separate access controls.

The commands below use the default Compose file. If the media worker is running, stop it with the same `compose.media.yaml` overlay and project name used to start it. Keep the image reference or source revision with the backup record.

```sh
docker compose stop app
umask 077
backup="$HOME/folderhug-backups/$(date +%Y%m%d-%H%M%S)"
key_backup="$HOME/folderhug-key-backups/$(basename "$backup")"
mkdir -p "$backup" "$key_backup"
docker compose run --rm --no-deps -T --entrypoint tar app -C /data -cpf - . > "$backup/data.tar"
docker compose run --rm --no-deps -T --entrypoint tar app -C /downloads -cpf - . > "$backup/downloads.tar"
docker compose --profile tools run --rm --no-deps -T --entrypoint tar setup -C /keys -cpf - . > "$key_backup/keys.tar"
```

Check each command's exit status and inspect all three archives with `tar -tf`. A failed command can leave an empty or incomplete archive. Restart the original core app with `docker compose start app` after the copies finish. For the media overlay, use its full Compose invocation to restart the app and worker.

For a native Windows installation, stop its process and copy the complete configured data, keys, and downloads directories to new protected backup directories. The same consistency and key requirements apply.

## Restore into a separate installation

Rehearse restoration into an unused Compose project and port. Inspect existing projects and volumes first; the example project name and port below must be free. Use the matching source revision or image reference before restoring. Set `backup` and `key_backup` to the matching archive directories in the current shell. Do not run owner setup on the restored database. Keep the explicit `-p folderhug-restore` on each command below.

These commands refuse a nonempty destination directory:

```sh
docker compose -p folderhug-restore --profile tools run --rm --no-deps -T --entrypoint sh setup -c 'test -z "$(ls -A /keys)" && tar -xpf - -C /keys' < "$key_backup/keys.tar"
docker compose -p folderhug-restore run --rm --no-deps -T --entrypoint sh app -c 'test -z "$(ls -A /data)" && tar -xpf - -C /data' < "$backup/data.tar"
docker compose -p folderhug-restore run --rm --no-deps -T --entrypoint sh app -c 'test -z "$(ls -A /downloads)" && tar -xpf - -C /downloads' < "$backup/downloads.tar"
export FOLDERHUG_PORT=18081
export FOLDERHUG_PUBLIC_URL=http://localhost:18081
docker compose -p folderhug-restore up -d app
```

Stop if any extraction fails. Confirm readiness, owner login, preferences, bookmarks, indexed results, and downloaded file checksums. Check a remembered profile with a bounded listing request if appropriate. Jobs interrupted by shutdown need an explicit resume or retry; a restored database is not evidence that an upstream representation is unchanged.

A restored database also restores its authentication records. Use owner recovery if those sessions and tokens must be revoked. A missing encryption key cannot be replaced by generating another key: the app checks the key against the database and refuses a mismatch. Restore the original matching key.

## Update and owner recovery

Before an update, stop and back up the installation and record its current image digest or source revision. For the default installation, download the reviewed replacement Compose file if it changed, run `docker compose pull`, then run `docker compose up -d app` with the same project name. Set `FOLDERHUG_IMAGE` to a specific tag or digest when you need to pin the app image; use that same value for setup and app commands. For a source build, select the intended source revision, rebuild with both Compose files, and start against the intended volumes. Database migrations run on startup. There are no down migrations; a rollback may require the old binary and its matching pre-update backup in a separate installation.

To change a forgotten owner password, stop the app and run the recovery command using the existing database and key:

```sh
docker compose stop app
docker compose --profile tools run --rm setup recover
docker compose up -d app
```

Recovery prompts for a new password and revokes all owner sessions and API tokens. It preserves the credential key. With a native build, stop the process and run `folderhug recover` with the same directory configuration. Neither recovery nor a routine update requires deleting volumes.

Keep any `FOLDERHUG_IMAGE` override set to the image reference already running for that installation during recovery. For a source build, add `-f compose.yaml -f compose.build.yaml` and keep the original project name on all three commands.
