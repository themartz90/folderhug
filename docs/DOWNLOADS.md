# Downloads

## Choose where the file goes

**Download to this device** (called **Download** in file details) streams through Folderhug to your browser's download manager. The browser saves the file on your device. No Docker host folder or mount is needed. Folderhug can report that it requested the download, but it cannot confirm when the browser finishes saving it.

**Save to server** creates a persistent job in Server queue. By default, completed files go into the app's `/downloads` volume. The owner can also choose a mounted host folder. API tokens can save only to the built-in `server` destination; they cannot choose a mounted folder or supply an arbitrary host path.

## Add a server folder

Make sure the folder exists on the Docker host. Under `services.app.volumes` in `compose.yaml`, keep the existing volumes and add a bind mount such as `- /mnt/drive:/mnt/drive:rw`. Folderhug discovers directory mounts directly under `/mnt` at startup and shows the final folder name, **drive** in this example, in the **Save to server** picker. Add one mount per host folder. The destination picker also lets the owner choose an existing subfolder inside a mounted root.

The first path in a bind mount is on the Docker host; the second is inside the container. They do not need to match: `- /srv/files:/mnt/files:rw` makes an existing `/srv/files` host folder available as **files**. The container path must be a direct child of `/mnt` for automatic discovery. The host folder must be writable by container UID/GID 10001; `:rw` does not change host permissions. Avoid broad ownership or permission changes across an existing media library. Short bind syntax can create a missing host folder, so check the path before running Compose. The [optional long-syntax example](../examples/compose.mounts.yaml) makes a missing host path an error.

Run `docker compose up -d` after adding a mount, then select it in **Save to server**. If you use a separate Compose override, include it again for later starts and updates. Mounted jobs stage their partial file in `/downloads` while they run, so that volume must have room for the largest active file. After a completed copy, the staged file is removed.

## Server queue and retries

The queue runs two jobs at a time. Each job uses one connection by default. Two or four connections require an exact source size, a strong ETag, a successful range probe, and a profile connection limit that permits the request. Other sources use one connection. Parallel connections can help with a limit applied to each connection; they do not remove a source-wide bandwidth limit.

Partial files stay inside a job-specific directory. Progress is recorded after syncing the file. Completion verifies the expected byte count, records a SHA-256 publication intent, and publishes without replacing an existing file. Startup checks interrupted publication and marks unfinished transfers as interrupted. It does not automatically restart an interrupted transfer.

Resume requires the same source generation, strong ETag, size, and validated segment positions. If that cannot be established, the API returns `restart_required`. Resuming with `allow_restart: true` explicitly allows discarding the partial contents and starting from zero. Retry is available for failed or cancelled jobs. If the download finished but saving to a mounted folder failed, retry verifies and reuses the staged file. Other retries start from zero.

Temporary credentials require their original live session or token lease. Remembered credentials allow an owner-created job to continue after logout. Revoked or expired API tokens stop their work. When the owner explicitly resumes or retries a token-created job, that job becomes owner-owned. Editing the source URL or deleting its profile cancels affected work. Completed local files survive profile deletion and remain available to the owner.

Creating a job requires an `Idempotency-Key` header of 1 to 128 printable ASCII characters. For 24 hours, repeating the same creator/key/payload returns the original accepted response. A different payload with that key returns a conflict. Read the job endpoint for current progress. After expiry, a new request can create a new job.

The queue admits at most 1,000 unfinished jobs, including paused and interrupted jobs. Completed, failed, and cancelled history does not count toward that limit. File cleanup is explicit: deleting a partial or completed local file never affects the upstream file. Partial cleanup is refused while its job is active. For a pending mounted copy, removing partial data clears both temporary locations and requires the mount to be available.

## Mount behavior and advanced configuration

Automatic discovery includes writable directory mounts directly under `/mnt`. Read-only mounts and folders used for Folderhug's own data, keys, or cache are excluded. The picker does not follow symlinks or cross into nested mounts. Mount each destination separately under `/mnt` if it lives on another filesystem.

You can keep mounts in a separate Compose file if you prefer. The included [mount override](../examples/compose.mounts.yaml) uses Docker's long syntax with `create_host_path: false`, so a missing host directory stops startup. Short syntax can create a missing host directory. See [Docker's bind-mount syntax](https://docs.docker.com/reference/compose-file/services/#volumes).

```yaml
services:
  app:
    volumes:
      - type: bind
        source: /mnt/drive
        target: /mnt/drive
        bind:
          create_host_path: false
```

Replace the source with your existing host folder and keep the target directly under `/mnt` for automatic discovery. Save the example as `compose.mounts.yaml` beside `compose.yaml`, then run `docker compose -f compose.yaml -f compose.mounts.yaml up -d`. Include both files in later startup and update commands. For a source build, include `compose.build.yaml` before the mount override. Keep any `-p` project name already in use.

`FOLDERHUG_MOUNTED_ROOTS` remains available for explicit destination IDs and for native paths outside Docker's `/mnt` discovery. It accepts JSON records with `id`, `label`, and `path`. Existing IDs must stay stable because saved jobs and favorites refer to them. Ordinary Docker mounts directly under `/mnt` need no value in this setting.

On Windows native development, use an absolute Windows path in that setting for an existing local folder. The picker shows folders inside available mounts, remembers the last selected folder, and stores favorites with the owner's preferences. If a mount is missing, it stays unavailable instead of switching a job to another destination. The owner can browse and download completed files after the mount returns. Folderhug does not delete completed files from mounted folders through Server queue, since another program may have changed or replaced them. Remove those files using the host's normal file tools.

Startup retries staged-file cleanup after an interrupted shutdown.

Copying to a mounted folder leaves the other queue controls available. Pause or cancel can stop the copy before its final filename is published. After a restart, an unfinished mounted copy waits for Resume; paused and cancelled jobs retain their state. Existing folder names are preserved when browsing a mount, while new download filenames are normalized for Windows and Linux. Favorites can be removed even when their folder or mount is unavailable.

## Storage limits

The built-in destination ID is `server`, backed by `FOLDERHUG_DOWNLOADS_DIR`. Download filenames are normalized for Windows and Linux, and mounted files keep both names when a filename already exists.

| Variable | Default | Meaning |
| --- | --- | --- |
| `FOLDERHUG_DOWNLOAD_BUDGET_BYTES` | 107374182400 (100 GiB) | Logical storage budget shared by queued transfers and existing local files. |
| `FOLDERHUG_DOWNLOAD_RESERVE_BYTES` | 5368709120 (5 GiB) | Free disk space to leave available. Zero disables this reserve. |
| `FOLDERHUG_DOWNLOAD_UNKNOWN_BYTES` | 21474836480 (20 GiB) | Maximum size reserved for a response whose length is unknown. |
| `FOLDERHUG_STREAM_TIMEOUT` | 24h | Request timeout; accepts durations from 1s through 168h. |

Reservations include unwritten portions of segmented files. Unknown-size downloads need enough space for their configured cap before starting. Set that cap at or below the total storage budget if you reduce the defaults. Another application can still consume disk space after a check; write errors leave an incomplete job, not a successful file.

Unknown source length remains `total_bytes: null` in the API even after completion. `transferred_bytes` records the bytes actually saved. Speed and ETA are estimates based on durable progress, so short jobs may have no estimate.

Completed file responses support a single byte range, attachment disposition, and live authorization cancellation. Filenames are display data, not paths that clients can choose.
