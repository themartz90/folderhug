# Compose operations and backups

The core deployment uses one app container and SQLite. The optional video poster worker is described in [MEDIA_WORKER.md](MEDIA_WORKER.md). These examples use a Linux shell from the repository root. `compose.yaml` builds from source; `compose.images.yaml` is a standalone configuration for an exact app image reference. No public image is available yet.

## First start from source

```sh
docker compose build app setup
docker compose --profile tools run --rm setup
docker compose up -d app
docker compose ps
```

`compose.yaml` uses the `http-wrapper` Compose project name. Keep that name when updating an existing source installation so Compose selects the same named volumes.

## Candidate image installation

When a verified image tag or digest is supplied, set `FOLDERHUG_IMAGE` to that exact reference and use `compose.images.yaml` by itself. The same reference is used for owner setup and the app. Registry access is required while an image is private:

```sh
export FOLDERHUG_IMAGE='<exact image tag or digest>'
docker compose -f compose.images.yaml -p folderhug pull
docker compose -f compose.images.yaml -p folderhug --profile tools run --rm setup
docker compose -f compose.images.yaml -p folderhug up -d app
docker compose -f compose.images.yaml -p folderhug ps
```

The example reference above is a placeholder, not a public image reference. The image file uses the `folderhug` Compose project name for a fresh installation. If you are moving an existing source installation to an image, add `-p http-wrapper` to each image command after confirming that it is the original project name. This keeps the existing volumes. Do not run source and image app containers against the same database at once.

The remaining commands on this page use the source Compose file. For an image installation, keep `-f compose.images.yaml` and the installation's project name on every backup, update, and recovery command. A fresh image installation uses `-p folderhug`; a migrated source installation normally uses `-p http-wrapper`. For example, `docker compose stop app` becomes `docker compose -f compose.images.yaml -p folderhug stop app` on a fresh image installation. Use the same prefix for `run`, `start`, `up`, `pull`, and `logs`. A separate restore intentionally uses a new project name, as shown below.

Setup prompts for an owner password of at least 12 characters. It refuses to replace an existing owner. The default address is `http://localhost:8080`, bound to the host's loopback interface. To select another local port, set both values before running Compose:

```sh
export HTTPWRAPPER_PORT=18080
export HTTPWRAPPER_PUBLIC_URL=http://localhost:18080
```

A different Compose project name selects different named volumes. Do not run multiple app instances against the same database.

`/health/live` reports that the HTTP server is responding. `/health/ready` also checks the database. Neither checks upstream credentials or source availability. `docker compose logs --tail=100 app` shows recent app logs; inspect logs before sharing them.

## Configuration

The native app reads environment variables directly. It does not load a `.env` file. Compose performs its normal variable interpolation, but only variables declared in the Compose service reach the container. Use an override file with `services.app.environment` for additional settings.

| Variable | Native default | Purpose |
| --- | --- | --- |
| `HTTPWRAPPER_ADDR` | `127.0.0.1:8080` | Listening address; Compose sets `0.0.0.0:8080` inside its container |
| `HTTPWRAPPER_PUBLIC_URL` | `http://localhost:8080` | Exact public origin for Host, Origin, and cookie checks |
| `HTTPWRAPPER_DATA_DIR` | `.local/data` | SQLite database and related files |
| `HTTPWRAPPER_KEYS_DIR` | `.local/keys` | Credential encryption key |
| `HTTPWRAPPER_DOWNLOADS_DIR` | `.local/downloads` | Completed and partial server downloads |
| `HTTPWRAPPER_CACHE_DIR` | `.local/cache` | Disposable thumbnail cache |
| `HTTPWRAPPER_STREAM_MAX_BYTES` | `1099511627776` | Maximum bytes per proxied source stream |
| `HTTPWRAPPER_STREAM_TIMEOUT` | `24h` | Stream deadline, from `1s` to `168h` |
| `HTTPWRAPPER_DOWNLOAD_BUDGET_BYTES` | `107374182400` | Download storage budget, 100 GiB |
| `HTTPWRAPPER_DOWNLOAD_RESERVE_BYTES` | `5368709120` | Free-space reserve, 5 GiB; zero disables this check |
| `HTTPWRAPPER_DOWNLOAD_UNKNOWN_BYTES` | `21474836480` | Reservation for an unknown-length download, 20 GiB |
| `HTTPWRAPPER_THUMBNAIL_CACHE_BYTES` | `1073741824` | Thumbnail cache budget, 1 GiB |
| `HTTPWRAPPER_UPSTREAM_ALLOW_FILE` | unset | Operator-controlled private-network allow file |
| `HTTPWRAPPER_MEDIA_SOCKET` | unset | Optional worker broker socket |
| `HTTPWRAPPER_WORKER_KEY` | `.local/worker-key/worker.key` | Separate app-to-worker authentication key |

Compose mounts `/data`, `/keys`, `/downloads`, and `/cache` from named volumes. It mounts `/keys` read-only in the app. The setup service writes the key as UID/GID 10001. Keep that ownership when restoring files. A writable host folder used as a mounted download destination also needs access for UID/GID 10001; check its existing permissions before changing ownership or mode.

The public URL is an origin, with no path, query, fragment, or credentials. A reverse proxy must preserve the public Host and Origin headers and forward to the loopback app port. Configure HTTPS at that proxy, disable response buffering for streaming downloads, and allow long responses and server-sent events. Forwarded headers do not replace the app's configured origin. Deployment under a URL subpath is not supported.

[examples/nginx.conf](../examples/nginx.conf) is a complete example configuration for nginx on the same host as the loopback Compose port. Replace `files.example.test` and the certificate paths, provide a valid certificate and key, and set `HTTPWRAPPER_PUBLIC_URL=https://files.example.test` before starting the app. If nginx already serves other applications, adapt the server block into that installation rather than replacing its entire configuration. A proxy in another container needs a different upstream address: its own `127.0.0.1` is not the host's app port.

The example keeps Host unchanged and lets nginx forward Origin, authorization, cookies, and range headers. It uses HTTP/1.1, disables response buffering and caching, and allows a 24-hour gap between upstream reads for long downloads and event streams. These directives follow the [nginx proxy module documentation](https://nginx.org/en/docs/http/ngx_http_proxy_module.html).

Private upstream addresses are rejected by default. An operator may provide a JSON allow file with exact host, port, and CIDR rules, for example:

```json
[{"host":"nas.example.test","port":8080,"cidrs":["192.168.1.0/24"]}]
```

Mount that file read-only and set `HTTPWRAPPER_UPSTREAM_ALLOW_FILE` to its container path. The file is limited to 64 KiB and 128 rules. Rules do not permit loopback, link-local, or metadata destinations. Do not turn a broad network range into a substitute for choosing the intended source.

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

The commands below use the source-build Compose file. For an image installation, apply the image file and project prefix described above to every command. Keep `FOLDERHUG_IMAGE` set to the exact image reference used by that installation. The image configuration does not include a media worker.

```sh
docker compose stop app
umask 077
backup="$HOME/http-wrapper-backups/$(date +%Y%m%d-%H%M%S)"
key_backup="$HOME/http-wrapper-key-backups/$(basename "$backup")"
mkdir -p "$backup" "$key_backup"
docker compose run --rm --no-deps -T --entrypoint tar app -C /data -cpf - . > "$backup/data.tar"
docker compose run --rm --no-deps -T --entrypoint tar app -C /downloads -cpf - . > "$backup/downloads.tar"
docker compose --profile tools run --rm --no-deps -T --entrypoint tar setup -C /keys -cpf - . > "$key_backup/keys.tar"
```

Check each command's exit status and inspect all three archives with `tar -tf`. A failed command can leave an empty or incomplete archive. Restart the original core app with `docker compose start app` after the copies finish. For the media overlay, use its full Compose invocation to restart the app and worker.

For a native Windows installation, stop its process and copy the complete configured data, keys, and downloads directories to new protected backup directories. The same consistency and key requirements apply.

## Restore into a separate installation

Rehearse restoration into an unused Compose project and port. Inspect existing projects and volumes first; the example project name and port below must be free. Use the matching source revision or exact image reference before restoring. Set `backup` and `key_backup` to the matching archive directories in the current shell. Do not run owner setup on the restored database. For an image restore, add `-f compose.images.yaml` to each restore command and keep the explicit `-p http-wrapper-restore` shown below.

These commands refuse a nonempty destination directory:

```sh
docker compose -p http-wrapper-restore --profile tools run --rm --no-deps -T --entrypoint sh setup -c 'test -z "$(ls -A /keys)" && tar -xpf - -C /keys' < "$key_backup/keys.tar"
docker compose -p http-wrapper-restore run --rm --no-deps -T --entrypoint sh app -c 'test -z "$(ls -A /data)" && tar -xpf - -C /data' < "$backup/data.tar"
docker compose -p http-wrapper-restore run --rm --no-deps -T --entrypoint sh app -c 'test -z "$(ls -A /downloads)" && tar -xpf - -C /downloads' < "$backup/downloads.tar"
export HTTPWRAPPER_PORT=18081
export HTTPWRAPPER_PUBLIC_URL=http://localhost:18081
docker compose -p http-wrapper-restore up -d app
```

Stop if any extraction fails. Confirm readiness, owner login, preferences, bookmarks, indexed results, and downloaded file checksums. Check a remembered profile with a bounded listing request if appropriate. Jobs interrupted by shutdown need an explicit resume or retry; a restored database is not evidence that an upstream representation is unchanged.

A restored database also restores its authentication records. Use owner recovery if those sessions and tokens must be revoked. A missing encryption key cannot be replaced by generating another key: the app checks the key against the database and refuses a mismatch. Restore the original matching key.

## Update and owner recovery

Before an update, stop and back up the installation and record its source revision or image digest. For a source build, build the selected revision and start it with the same Compose project name. For an image installation, set `FOLDERHUG_IMAGE` to the reviewed replacement reference, then use `docker compose -f compose.images.yaml -p <installation-project> pull` and `docker compose -f compose.images.yaml -p <installation-project> up -d app`. Replace `<installation-project>` with `folderhug` for a fresh image installation or the original project name for a migrated one. Database migrations run on startup. There are no down migrations; a rollback may require the old binary and its matching pre-update backup in a separate installation.

To change a forgotten owner password, stop the app and run the recovery command using the existing database and key:

```sh
docker compose stop app
docker compose --profile tools run --rm setup recover
docker compose up -d app
```

Recovery prompts for a new password and revokes all owner sessions and API tokens. It preserves the credential key. With a native build, stop the process and run `http-wrapper recover` with the same directory configuration. Neither recovery nor a routine update requires deleting volumes.

On an image installation, run the same three recovery steps with `docker compose -f compose.images.yaml -p <installation-project>` in place of `docker compose`. Keep `FOLDERHUG_IMAGE` set to the exact image reference already running for that installation.
