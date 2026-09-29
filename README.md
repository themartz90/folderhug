# Folderhug

![Folderhug, a red panda holding a folder](docs/assets/banner-light.jpg)

Folderhug is a self-hosted file explorer for HTTP directory listings, such as a seedbox's download folder. Add your sources, browse and search their folders, preview supported media, and download files to your browser or server. Folderhug leaves the source files unchanged.

You can switch between list and grid views, filter a folder, save filter presets, pin folders, and use Find files to search the current folder or its subfolders. Server downloads run in a persistent queue. Folderhug has one owner account and scoped API tokens for other clients. Image thumbnails work in the app; video posters use an optional isolated worker. Playback depends on your browser and the file's codec.

## Install with Docker Compose

On a Linux host with Docker Compose, download the Compose file and start Folderhug:

```sh
mkdir folderhug
cd folderhug
curl -fL https://raw.githubusercontent.com/themartz90/folderhug/main/compose.yaml -o compose.yaml
docker compose up -d
```

Open `http://localhost:8080` on the host and create the owner password in the browser. The first visitor to a fresh installation can claim the owner account, so complete setup before exposing the address to anyone else. The password needs at least 6 characters, with no required character types, and can be up to 1,024 UTF-8 bytes.

The app creates its credential key on first start. Keep it with your backups; the app needs the same key to open an existing installation. The default port binds to loopback. Remote access needs an HTTPS reverse proxy and a matching public URL. See [operations and backups](docs/OPERATIONS.md) before changing the address or upgrading an installation.

A normal sign-in lasts up to 30 days. **Remember me** keeps the session renewable for up to 90 days without activity, with a one-year limit from login.

The default Compose file pulls `ghcr.io/themartz90/folderhug:latest`. Set `FOLDERHUG_IMAGE` when you need a specific tag or digest. The [optional media worker](docs/MEDIA_WORKER.md) uses a separate Compose file.

To build from source, clone the [repository](https://github.com/themartz90/folderhug) and use the `compose.build.yaml` override described in [operations](docs/OPERATIONS.md).

## Save to a host folder

The default server destination is the `/downloads` volume. To let the owner choose a host folder, create a `compose.mounts.yaml` override alongside `compose.yaml`:

```yaml
services:
  app:
    environment:
      FOLDERHUG_MOUNTED_ROOTS: '[{"id":"media","label":"Media","path":"/mnt/media"}]'
    volumes:
      - type: bind
        source: /srv/media
        target: /mnt/media
        bind:
          create_host_path: false
```

Create `/srv/media` first and give the container's UID/GID 10001 write access. Check existing media permissions before changing them. Then start with `docker compose -f compose.yaml -f compose.mounts.yaml up -d` and keep both files in later update commands. Mounted jobs stage a copy in `/downloads`, so that volume also needs space for the file. See [downloads and mounted folders](docs/DOWNLOADS.md) for limits and resume behavior.

## Local development

Use Go 1.27.1 and Node 24. Build the frontend first because Go embeds its output:

```powershell
cd web
npm ci
npm run build
cd ..
go run ./cmd/folderhug dev
```

Open `http://localhost:8080` and add a directory listing URL. Development mode signs you in automatically, uses separate state in `.local/dev`, and listens on loopback only. For a normal owner account outside development mode, run `go run ./cmd/folderhug`, then create the password in the browser. The CLI `setup` command remains available for automation.

For hot reload, tests, and browser fixtures, see [Windows development](docs/DEVELOPMENT.md). Native development does not need Docker or WSL.

## What to expect

- Standard Apache table/preformatted and nginx listings are supported. Custom listing pages may need a parser change.
- **Download** uses the browser's download manager. **Save to server** creates a background job in Folderhug's Server queue. The owner can save into folders exposed as Compose mounts.
- API tokens can use only the built-in server destination.
- Find files lets you search the current folder or its subfolders. Crawls are explicit and bounded; filtering the resulting files does not start another crawl.
- Temporary source credentials stay in server memory. Remembered credentials are encrypted with a key stored outside the database.
- There are no uploads, upstream rename/delete actions, public shares, multiple user accounts, telemetry, or transcoding.

Read [source compatibility](docs/COMPATIBILITY.md), [downloads and mounted folders](docs/DOWNLOADS.md), and [API examples](docs/API_USAGE.md) for details. Runtime settings use the `FOLDERHUG_` prefix.

## License

[Contributing](CONTRIBUTING.md) covers development changes. See [Security](SECURITY.md) for vulnerability reporting and deployment boundaries. Project code uses the [MIT license](LICENSE); dependencies retain their own licenses, listed in [third-party notices](docs/THIRD_PARTY_NOTICES.md).
