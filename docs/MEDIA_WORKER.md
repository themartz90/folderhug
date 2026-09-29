# Media worker

Folderhug can show image thumbnails without a second service. Video posters need the optional media worker. It extracts one JPEG frame through a bounded broker connection; the app remains responsible for upstream requests. A poster may be unavailable for a valid video, and a poster does not mean the browser can play that file.

## Run the worker from source

Start the core app and create the owner account first. Then create the worker key once and start the media profile:

```sh
docker compose --profile tools run --rm media-setup
docker compose -f compose.yaml -f compose.media.yaml --profile media up -d --build
```

The key setup refuses to replace an existing key. The worker uses that dedicated key, not the owner's credential key. To return to the core app without the worker:

```sh
docker compose -f compose.yaml -f compose.media.yaml --profile media stop media
docker compose -f compose.yaml up -d app
```

The owner-only `/api/v1/status` endpoint reports whether the worker has polled recently. The worker currently has a source-build Compose path. `compose.images.yaml` contains only the core app and setup service; no public worker image is available.

## What it can process

Video poster extraction requires a strong ETag and verified byte ranges. The worker recognizes MOV/MP4, Matroska/WebM, and AVI. It reads at most 32 MiB across requests, runs for at most 15 seconds, and produces a JPEG no larger than 320 by 320 pixels. Videos outside those limits fall back to a file-type icon. The app does not transcode video.

Image thumbnails accept JPEG, PNG, and the first GIF frame. The app applies JPEG orientation metadata and limits compressed input to 16 MiB, decoded images to 16 million pixels, and raster memory to 64 MiB. WebP input currently has no thumbnail decoder. `HTTPWRAPPER_THUMBNAIL_CACHE_BYTES` defaults to 1 GiB. The cache can be rebuilt after a restart.

Playback depends on the browser, container, and codecs. If playback fails or takes too long to load, the media view keeps the browser download action available.

## Isolation and configuration

The supplied `compose.media.yaml` gives the worker no network access, a read-only filesystem, dropped capabilities, and CPU, memory, and process limits. It mounts only the broker socket and dedicated worker key. Do not add the database, credential key, upstream credentials, or downloads directory to the worker. FFmpeg is a separate process, and these container restrictions are part of its boundary.

The worker reads `HTTPWRAPPER_MEDIA_SOCKET` for the broker Unix socket and `HTTPWRAPPER_WORKER_KEY` for a file containing exactly 32 raw key bytes. `FFMPEG_PATH` optionally selects the executable and defaults to `ffmpeg`. Configuration errors stop startup. SIGINT and SIGTERM cancel the current request and FFmpeg process.

The source-build worker image uses Debian's pinned FFmpeg package. FFmpeg and its libraries keep their own licenses, separate from Folderhug's MIT license. Review [third-party notices](THIRD_PARTY_NOTICES.md) and provide corresponding source and license material for the exact packages before distributing a binary worker image.
