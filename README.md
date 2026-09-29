# Folderhug

Folderhug is a self-hosted file explorer for HTTP directory listings, such as a seedbox's download folder. Add your sources, browse and search their folders, preview supported media, and download files to your browser or server. Folderhug leaves the source files unchanged.

You can switch between list and grid views, filter a folder, save filter presets, pin folders, and use Find files to search the current folder or its subfolders. Server downloads run in a persistent queue. Folderhug has one owner account and scoped API tokens for other clients. Image thumbnails work in the app; video posters use an optional isolated worker. Playback depends on your browser and the file's codec.

No public container image or release is available yet. The source build instructions below are the current installation path.

## Run from source with Compose

Clone the repository, then build and start the app on a Linux host with Docker Compose. While the repository is private, cloning requires access to it.

```sh
git clone https://github.com/themartz90/folderhug.git
cd folderhug
docker compose build
docker compose --profile tools run --rm setup
docker compose up -d app
```

Setup asks for an owner password of at least 12 characters. Open `http://localhost:8080` and sign in. The default port binds to loopback; remote access needs an HTTPS reverse proxy and a matching public URL. The database, credential key, downloads, and thumbnail cache use separate named volumes. See [operations and backups](docs/OPERATIONS.md) before changing the address or upgrading an installation.

An image-based Compose configuration is in `compose.images.yaml`. It requires an exact `FOLDERHUG_IMAGE` reference for both setup and the app. No public image is available to install from yet. The [optional media worker](docs/MEDIA_WORKER.md) currently uses a separate source build.

## Local development

Use Go 1.27.1 and Node 24. Build the frontend first because Go embeds its output:

```powershell
cd web
npm ci
npm run build
cd ..
go run ./cmd/http-wrapper dev
```

Open `http://localhost:8080` and add a directory listing URL. Development mode signs you in automatically, uses separate state in `.local/dev`, and listens on loopback only. For an owner account outside development mode, use `go run ./cmd/http-wrapper setup` followed by `go run ./cmd/http-wrapper`. Setup asks for a password of at least 12 characters. Keep its credential key with your backups; losing it prevents access to remembered source passwords.

For hot reload, tests, and browser fixtures, see [Windows development](docs/DEVELOPMENT.md). Native development does not need Docker or WSL.

## What to expect

- Standard Apache table/preformatted and nginx listings are supported. Custom listing pages may need a parser change.
- **Download** uses the browser's download manager. **Save to server** creates a background job in Folderhug's Server queue. The owner can save into folders exposed as Compose mounts.
- Mounted jobs stage temporary data in `/downloads` before copying to the selected folder, so that volume needs room for the file. API tokens can use only the built-in server destination.
- Find files lets you search the current folder or its subfolders. Crawls are explicit and bounded; filtering the resulting files does not start another crawl.
- Temporary source credentials stay in server memory. Remembered credentials are encrypted with a key stored outside the database.
- There are no uploads, upstream rename/delete actions, public shares, multiple user accounts, telemetry, or transcoding.

Read [source compatibility](docs/COMPATIBILITY.md), [downloads and mounted folders](docs/DOWNLOADS.md), and [API examples](docs/API_USAGE.md) for details. Runtime settings retain the `HTTPWRAPPER_` prefix.

## License

[Contributing](CONTRIBUTING.md) covers development changes. [Security](SECURITY.md) records the current reporting limitation and deployment boundaries. Project code uses the [MIT license](LICENSE); dependencies retain their own licenses, listed in [third-party notices](docs/THIRD_PARTY_NOTICES.md).
