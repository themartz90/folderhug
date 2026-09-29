# Source compatibility and limits

Folderhug reads HTTP directory listings. A profile points to one HTTP or HTTPS directory root. The app can use no authentication or HTTP Basic authentication. Plain HTTP requires an explicit profile opt-in because it sends credentials and content without transport encryption. Form logins, OAuth, JavaScript-only listings, and arbitrary websites are not supported source types.

## Listings and paths

The parser supports Apache table and preformatted listings and nginx listings covered by the fixtures in this repository. It requires an "Index of /" heading and recognized listing structure. It reads link targets as data and does not execute upstream HTML or scripts. A server calling itself Apache or nginx is not enough to establish compatibility; themes, custom markup, proxy rewrites, and authentication pages can change the response. A listing response is limited to 8 MiB and 50,000 entries.

Use the profile's Test action to check access and listing parsing. That test does not establish byte-range support for every file. Single-stream downloads work when a usable content response is available. Multiple connections need a separate checked range response, an exact size, a strong ETag, and an unchanged representation.

Links must stay on the approved origin and under its configured root path. Parent links, traversal, encoded separators, backslashes, unsafe redirects, and outside-root links are rejected. Display names and encoded source paths are separate. The app uses opaque profile-bound entry IDs for operations, never an arbitrary proxy URL supplied by a caller. Changing a profile URL invalidates its old entry IDs and stops affected work.

Private addresses are rejected unless the operator configures a narrow host/port/CIDR allowance. Loopback, link-local, and metadata destinations remain blocked. DNS and redirect checks also apply to later downloads, scans, and previews. A private source may therefore need explicit operator configuration even when the browser can reach it directly.

Listing sizes and timestamps can be absent or approximate. The interface labels approximate sizes, and an unknown size is not zero. Size filters use the parsed value, so a match based on an approximate listing size does not establish an exact byte count. Sorting and filtering use the available parsed metadata, with missing values kept distinct.

## Index and downloads

Current-folder filters apply to one listing. Indexed search uses locally recorded scan entries; it does not fetch the whole source on each query. Its coverage can be absent, partial, complete, or stale. A scan stops at its configured time, depth, and entry limits. Interrupted scans preserve earlier indexed data without treating unvisited entries as deleted.

Scans allow at most 64 levels, 100,000 entries, and 1,800 seconds. Directory exclusions and category selection affect the recorded result and coverage. An empty indexed name query can list or filter all authorized indexed entries in the selected scope. Large folders and scans use cursor pagination, with a maximum page size of 1,000.

Browser downloads use the browser's download manager. The app cannot confirm when the browser saved a file. Server downloads run as persistent jobs and store files in the operator's configured destination. There are two active server jobs at a time and up to 1,000 unfinished jobs. Per-file connections are 1, 2, or 4; the app falls back to one stream when safe segmentation cannot be established. There is no archive-on-the-fly folder download. Scan a folder, review its file entries, and queue the selected files.

Partial downloads are not published as completed files. After a restart, interrupted jobs need an explicit action. Resume depends on source identity and representation checks; a changed source can require restarting from zero. Completed files remain available to the owner after their profile is removed. Deleting a local file does not delete its upstream original.

## Previews and deployment

Image thumbnails accept JPEG, PNG, and GIF input. They use the first GIF frame and apply JPEG orientation metadata. Input and decoded-image limits can make a large image unavailable; the file remains downloadable. WebP thumbnail decoding is not included. Video posters require the optional isolated FFmpeg worker. Its byte and time limits mean that some valid videos have no poster.

Playback depends on the browser, container, and codecs. A poster is not proof that the browser can play the video. The media view handles playback errors and a load timeout and keeps a browser-download action available. The app does not transcode media. See [media worker](MEDIA_WORKER.md) for setup and limits.

Native development uses Windows 11. Deployment uses the supplied Linux Compose files. ARM64 and other targets have not been verified. The app has one owner account, scoped API tokens, and one SQLite database. It does not provide multiple users, public sharing, uploads, upstream rename/delete, or horizontal scaling.
