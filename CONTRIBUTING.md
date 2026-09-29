# Contributing

Folderhug is being prepared for its first public release. A public contribution channel has not been opened yet. These notes cover work on a local checkout.

Use [Windows development](docs/DEVELOPMENT.md) for native setup and checks.

Keep changes focused and explain the behavior being changed. Add regression tests when a change affects parsing, credentials, authorization, network boundaries, persistent jobs, ranges, or path safety. Use synthetic fixtures. Private source URLs, credentials, runtime databases, downloaded content, and browser traces do not belong in a patch.

Run the checks relevant to the change and report their actual result. `scripts/check.ps1` covers native Go and frontend checks; browser tests and Linux race/container checks are separate. If a check cannot run, say which one and why. Do not label a planned check as passed.

When changing the API, update `api/openapi.yaml`, regenerate the TypeScript contract, and run its drift check. Add a new migration for a database schema change; do not edit an applied migration. Preserve encoded source paths separately from display names and local filesystem paths.

Upstream access must remain read-only and use the checked transport. Keep credentials on the server, stream file content with cancellation and backpressure, and preserve authorization while jobs run. New network access, worker mounts, dependencies, and distribution changes need an explicit review of those boundaries.

Project code is MIT licensed. Update [third-party notices](docs/THIRD_PARTY_NOTICES.md) when shipped dependencies change. The optional FFmpeg worker has additional binary distribution requirements. See [Security](SECURITY.md) for the current reporting limitation.
