# Downloads

Browser downloads stream through the app to the browser's normal download manager. The app does not know when the browser has saved a file successfully. Server downloads are separate persistent jobs, shown in Server queue and available through the authenticated API.

## Server queue

The queue runs two jobs at a time. Each job uses one connection by default. Two or four connections require an exact source size, a strong ETag, a successful range probe, and a profile connection limit that permits the request. Other sources use one connection. Parallel connections can help with a limit applied to each connection; they do not remove a source-wide bandwidth limit.

Partial files stay inside a job-specific directory. Progress is recorded after syncing the file. Completion verifies the expected byte count, records a SHA-256 publication intent, and publishes without replacing an existing file. Startup checks interrupted publication and marks unfinished transfers as interrupted. It does not automatically restart an interrupted transfer.

Resume requires the same source generation, strong ETag, size, and validated segment positions. If that cannot be established, the API returns `restart_required`. Resuming with `allow_restart: true` explicitly allows discarding the partial contents and starting from zero. Retry is available for failed or cancelled jobs. If the download finished but saving to a mounted folder failed, retry verifies and reuses the staged file. Other retries start from zero.

Temporary credentials require their original live session or token lease. Remembered credentials allow an owner-created job to continue after logout. Revoked or expired API tokens stop their work. When the owner explicitly resumes or retries a token-created job, that job becomes owner-owned. Editing the source URL or deleting its profile cancels affected work. Completed local files survive profile deletion and remain available to the owner.

Creating a job requires an `Idempotency-Key` header of 1 to 128 printable ASCII characters. For 24 hours, repeating the same creator/key/payload returns the original accepted response. A different payload with that key returns a conflict. Read the job endpoint for current progress. After expiry, a new request can create a new job.

The queue admits at most 1,000 unfinished jobs, including paused and interrupted jobs. Completed, failed, and cancelled history does not count toward that limit. File cleanup is explicit: deleting a partial or completed local file never affects the upstream file. Partial cleanup is refused while its job is active. For a pending mounted copy, removing partial data clears both temporary locations and requires the mount to be available.

## Storage limits

The built-in destination ID is `server`, backed by `FOLDERHUG_DOWNLOADS_DIR`. The owner can also configure mounted folders with `FOLDERHUG_MOUNTED_ROOTS`. A job then supplies a configured destination ID and an existing folder path relative to that mount. API tokens remain limited to `server`. The app does not accept an arbitrary host path in a download request. Download filenames are normalized for Windows and Linux, and mounted files keep both names when a filename already exists.

Configure a host folder with a normal Compose bind mount, such as `- /mnt/drive:/mnt/drive:rw`, then list its container path in `FOLDERHUG_MOUNTED_ROOTS`. The [README](../README.md#save-to-a-host-folder) shows both edits in the main Compose file. The mount makes the folder accessible to the container; the setting gives it a name in the folder picker.

The first mount path is on the host, and the second is inside the container. They can be identical. If you prefer `/mnt/drive:/media:rw`, use `/media` as the JSON `path`. The `label` is shown in the app. Keep each `id` unique and stable, since saved jobs and favorites refer to it.

For multiple folders, add a mount for each one and list them in the same JSON array:

```yaml
FOLDERHUG_MOUNTED_ROOTS: '[{"id":"drive","label":"Drive","path":"/mnt/drive"},{"id":"music","label":"Music","path":"/mnt/Music"}]'
```

Each host folder needs write permission for the container's UID/GID 10001. The `:rw` option permits writes through the mount, but does not change host filesystem permissions. For an existing library, grant access to the intended folder without changing ownership or permissions across the whole tree.

### Optional override and missing-path checks

You can keep mounts in a separate file if you prefer. The included [mount override](../examples/compose.mounts.yaml) uses Docker's long syntax with `create_host_path: false`, so a missing host directory stops startup. Short syntax creates the host directory if it does not exist. See [Docker's bind-mount syntax](https://docs.docker.com/reference/compose-file/services/#volumes).

```yaml
services:
  app:
    environment:
      FOLDERHUG_MOUNTED_ROOTS: '[{"id":"drive","label":"Drive","path":"/mnt/drive"}]'
    volumes:
      - type: bind
        source: /mnt/drive
        target: /mnt/drive
        bind:
          create_host_path: false
```

Replace `/mnt/drive` with your existing folder, keeping `target` and the JSON `path` equal. Save the example as `compose.mounts.yaml` beside `compose.yaml`, then run `docker compose -f compose.yaml -f compose.mounts.yaml up -d`. Include both files in later startup and update commands. For a source build, include `compose.build.yaml` before the mount override. Keep any `-p` project name already in use.

### Storage behavior

On Windows native development, set the same variable with an absolute Windows path to an existing local folder. The picker shows folders inside configured mounts, remembers the last selected folder, and stores favorites with the owner's preferences. If a mount is missing, it stays unavailable instead of switching a job to another destination. The owner can still browse and download completed files after the mount returns. The app does not delete completed files from mounted folders through Server queue, since another program may have changed or replaced them. Remove those files using the host's normal file tools.

A mounted job stages its partial file under `/downloads` while it runs, so that volume must have room for the largest active file. After completion, the app removes the staged copy. Startup also retries cleanup after an interrupted shutdown.

Copying to a mounted folder leaves the other queue controls available. Pause or cancel can stop the copy before its final filename is published. After a restart, an unfinished mounted copy waits for Resume; paused and cancelled jobs retain their state. Existing folder names are preserved when browsing a mount, while new download filenames use the portable naming rules above. Favorites can be removed even when their folder or mount is unavailable.

| Variable | Default | Meaning |
| --- | --- | --- |
| `FOLDERHUG_DOWNLOAD_BUDGET_BYTES` | 107374182400 (100 GiB) | Logical storage budget shared by queued transfers and existing local files. |
| `FOLDERHUG_DOWNLOAD_RESERVE_BYTES` | 5368709120 (5 GiB) | Free disk space to leave available. Zero disables this reserve. |
| `FOLDERHUG_DOWNLOAD_UNKNOWN_BYTES` | 21474836480 (20 GiB) | Maximum size reserved for a response whose length is unknown. |
| `FOLDERHUG_STREAM_TIMEOUT` | 24h | Request timeout; accepts durations from 1s through 168h. |

Reservations include unwritten portions of segmented files. Unknown-size downloads need enough space for their configured cap before starting. Set that cap at or below the total storage budget if you reduce the defaults. Another application can still consume disk space after a check; write errors leave an incomplete job, not a successful file.

Unknown source length remains `total_bytes: null` in the API even after completion. `transferred_bytes` records the bytes actually saved. Speed and ETA are estimates based on durable progress, so short jobs may have no estimate.

Completed file responses support a single byte range, attachment disposition, and live authorization cancellation. Filenames are display data, not paths that clients can choose.
