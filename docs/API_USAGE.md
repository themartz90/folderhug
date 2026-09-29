# API examples

The implemented contract is [api/openapi.yaml](../api/openapi.yaml). Paths start with `/api/v1`; JSON uses snake_case. These PowerShell 7 examples use an API token created in Settings. Choose only the profiles and scopes the script needs. The token secret is shown once.

## Connect and browse

Keep the token out of URLs and command history. Read it into this process and clear it when finished:

```powershell
$base = 'http://localhost:8080/api/v1'
$token = Read-Host 'API token' -MaskInput
$headers = @{ Authorization = "Bearer $token" }
$profiles = Invoke-RestMethod "$base/profiles" -Headers $headers
$profiles
```

Choose a profile ID from that response. Use the returned entry IDs for later requests; a filename or upstream URL is not an entry ID.

```powershell
$profileID = Read-Host 'Profile ID'
$profilePath = [uri]::EscapeDataString($profileID)
$listing = Invoke-RestMethod "$base/profiles/$profilePath/browse?directory_id=root&limit=200" -Headers $headers
$listing
```

Browsing requires `browse:read`. Current-folder filtering belongs on this listing request, for example `name=report&category=document&show_folders=false`. It does not search unvisited subfolders. URL-encode user-supplied parameter values with `[uri]::EscapeDataString()`.

Use `next_cursor` from a paginated response to fetch the next page with the same query. If the API returns `409 snapshot_expired`, start again without a cursor. Do not merge pages from different snapshots as if they were one listing.

## Scan and indexed search

A scan records entries in the local index. It does not download their content. Creating a scan requires `scan:write` and a new idempotency key:

```powershell
$scanHeaders = $headers.Clone()
$scanHeaders['Idempotency-Key'] = [guid]::NewGuid().ToString()
$scanBody = @{ directory_id = 'root'; max_depth = 4; max_entries = 10000; max_seconds = 300 } | ConvertTo-Json
$scan = Invoke-RestMethod "$base/profiles/$profilePath/scans" -Method Post -Headers $scanHeaders -ContentType 'application/json' -Body $scanBody
$scan
```

Save the returned scan ID. Read its status with `GET /scans/{id}` and inspect coverage before treating results as complete. A bounded, interrupted, failed, or excluded scan can leave partial coverage. `GET /scans/{id}/export?format=json` or `format=csv` exports its indexed results within the caller's permissions.

Indexed search requires `browse:read`. This example lists indexed document entries under the profile root without a name query:

```powershell
$results = Invoke-RestMethod "$base/search?profile_id=$profilePath&directory_id=root&category=document&limit=200" -Headers $headers
$results
```

Omitted and empty `q` have the same meaning. A nonempty `q` performs a name search; `match=substring` is the default and `match=prefix` selects a prefix. Scope search to a `profile_id` and, when needed, a `directory_id` from a listing. Directory scope includes that directory and its indexed descendants. Results can contain folders; select file entries before creating download jobs. A result does not prove that its source still exists.

## Queue one server download

The web UI's **Download to this device** action saves through the browser and needs no server destination. This example creates a **Save to server** job through the API. Creating and managing that job requires `downloads:write`. Use a file ID from a listing or indexed result. The destination ID `server` maps to the configured downloads directory. Owner sessions can also choose a discovered or explicitly configured mount and an existing folder relative to it with `destination_path`. API tokens remain limited to `server`; requests cannot supply an arbitrary host path.

```powershell
$entryID = Read-Host 'File entry ID'
$downloadHeaders = $headers.Clone()
$downloadHeaders['Idempotency-Key'] = [guid]::NewGuid().ToString()
$downloadBody = @{ profile_id = $profileID; entry_id = $entryID; destination_id = 'server'; connections = 1 } | ConvertTo-Json
$job = Invoke-RestMethod "$base/downloads" -Method Post -Headers $downloadHeaders -ContentType 'application/json' -Body $downloadBody
$job
```

Read `GET /downloads/{id}` for current progress. The accepted create response is not a completion result. If a creation request loses its response, retry with the same key and body. Keys last 24 hours; changing the payload with the same key returns a conflict. Repeating the request returns its original accepted response, so read the job endpoint afterward.

Use `content:read` to stream source content or a completed job's file. For source content the route is `/profiles/{profile_id}/entries/{entry_id}/content`; use `Invoke-WebRequest -OutFile` or a streaming client rather than collecting bytes in memory. Content requests support HEAD and a single byte range. See [DOWNLOADS.md](DOWNLOADS.md) for interruption, resume, and cleanup behavior.

## Authentication and errors

On a fresh installation, `GET /auth/setup` returns `setup_required: true` until the owner account is created. The first visitor can claim it with `POST /auth/setup`, so complete setup before exposing the app to anyone else. Setup accepts a password of at least 6 Unicode characters and at most 1,024 UTF-8 bytes, with no required character types. Once setup is complete, use `POST /auth/login`; setup will not reopen if the owner record or credential key is missing.

Login and setup accept `remember_me: true` to request a remembered browser session. A normal session lasts up to 30 days. A remembered session can renew to 90 days after activity, capped at 365 days from login. `POST /auth/refresh` renews an authenticated owner session; `POST /auth/logout-all` revokes all owner browser sessions but leaves API tokens active. Owner recovery revokes both sessions and API tokens.

| Scope | Access within the token's listed profiles |
| --- | --- |
| `browse:read` | Profiles, listings, metadata, indexed search, scan reads and exports |
| `content:read` | Source content, thumbnails, and accessible completed download content |
| `scan:write` | Create scans and control scans created by this token |
| `downloads:write` | Create, read, and control this token's server jobs |
| `credentials:unlock` | Supply a temporary credential lease for this token |

Scopes are independent. A download script that first browses needs both `browse:read` and `downloads:write`. Owner-only actions include profile configuration and testing, remembered credentials, token management, preferences, bookmarks, and completed-file deletion. Tokens cannot borrow an owner's temporary credentials or control another token's jobs. Revocation or expiry stops authorized in-flight work.

An invalid bearer token never falls back to an owner cookie. Cookie-based clients must send the `X-CSRF-Token` returned by `GET /auth/session` and the browser's `Origin` on authenticated mutations, including refresh and sign-out. Setup and login check `Origin` but do not require an existing CSRF token. By default, the app accepts localhost and literal IP origins at its listening port; `FOLDERHUG_PUBLIC_URL` pins it to one exact origin. Do not put either owner or source passwords in URLs.

Errors contain `code`, `message`, and `request_id`. A `401` means app authentication is missing or invalid; source authentication failures have a separate upstream error. Missing and inaccessible objects are not disclosed to token callers. Respect `Retry-After` on `429` and `503` responses.

When finished:

```powershell
$headers.Clear()
if ($scanHeaders) { $scanHeaders.Clear() }
if ($downloadHeaders) { $downloadHeaders.Clear() }
Remove-Variable token -ErrorAction SilentlyContinue
```
