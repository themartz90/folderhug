// Generated from api/openapi.yaml. Run npm run api:generate to update.
export interface paths {
    "/auth/setup": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getSetupStatus"];
        put?: never;
        /** @description Claim a fresh installation once. Requires a matching Origin and a JSON body. Passwords need at least six Unicode characters and at most 1024 UTF-8 bytes. */
        post: operations["setupOwner"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["login"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["logout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/session": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getSession"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listProfiles"];
        put?: never;
        post: operations["createProfile"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["getProfile"];
        put?: never;
        post?: never;
        /** @description Cancel jobs and remove metadata/credentials. Keep completed local files for the owner. */
        delete: operations["deleteProfile"];
        options?: never;
        head?: never;
        /** @description Omitted fields remain unchanged. Root changes stop work and invalidate cached IDs. */
        patch: operations["updateProfile"];
        trace?: never;
    };
    "/profiles/{id}/test": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["testProfile"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/unlock": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Create a principal-bound memory-only credential lease, cleared on logout, expiry or restart. */
        post: operations["unlockProfile"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/lock": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Clear the caller's lease and cancel its upstream requests. Does not delete remembered credentials. */
        post: operations["lockProfile"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/browse": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["browse"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/entries/{entry_id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
                entry_id: components["parameters"]["EntryID"];
            };
            cookie?: never;
        };
        get: operations["getEntry"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/entries/{entry_id}/content": {
        parameters: {
            query?: {
                /** @description Preview accepts only passive allowlisted media. HTML and SVG are never inline app-origin content. */
                disposition?: "attachment" | "preview";
            };
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
                "If-None-Match"?: components["parameters"]["IfNoneMatch"];
            };
            path: {
                id: components["parameters"]["ID"];
                entry_id: components["parameters"]["EntryID"];
            };
            cookie?: never;
        };
        get: operations["getEntryContent"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head: operations["headEntryContent"];
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/scans": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["createScan"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listScans"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["getScan"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}/pause": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["pauseScan"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}/resume": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["resumeScan"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["cancelScan"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}/retry": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Requeue failed or cancelled work while preserving the completed frontier. */
        post: operations["retryScan"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/scans/{id}/export": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["exportScan"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/search": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** @description Search indexed names/paths only. An omitted or empty q lists all authorized indexed entries in the requested scope and filters. This never starts a scan. */
        get: operations["searchIndex"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/destinations": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listDestinations"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/logout-all": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Revoke every owner session and clear their temporary source credentials. API tokens remain valid. Requires Origin and X-CSRF-Token. */
        post: operations["logoutAllDevices"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/refresh": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Renew a remembered session's 90-day inactivity deadline, up to one year after login. Rotate its cookie periodically. Normal sessions keep their original 30-day expiry. Requires Origin and X-CSRF-Token. */
        post: operations["refreshSession"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/destinations/{id}/folders": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listDestinationFolders"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listDownloads"];
        put?: never;
        post: operations["createDownload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["getDownload"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/pause": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["pauseDownload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/resume": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["resumeDownload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** @description Cancel work; keep partial output until the separate cleanup action. */
        post: operations["cancelDownload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/retry": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["retryDownload"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/partial": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /** @description Remove partial local output only after cancellation or failure. Tokens may clean up only their own jobs. */
        delete: operations["deletePartialDownload"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/downloads/{id}/content": {
        parameters: {
            query?: never;
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
            };
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        /** @description Completed local file. Tokens need profile access and ownership of the download job. */
        get: operations["getDownloadContent"];
        put?: never;
        post?: never;
        /** @description Explicitly delete the completed local file. Never affects the source file. */
        delete: operations["deleteDownloadedFile"];
        options?: never;
        head: operations["headDownloadContent"];
        patch?: never;
        trace?: never;
    };
    "/profiles/{id}/thumbnails": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post: operations["requestThumbnails"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/thumbnails/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get: operations["getThumbnail"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/events": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * @description Session-cookie or bearer-authenticated SSE. Each event has an opaque id, event name,
         *     and one JSON data object using Event. Events are scan, download, thumbnail or reset.
         *     Scan events require browse:read; download events require downloads:write and job
         *     ownership; thumbnail events require content:read. Every event is profile-filtered.
         *     Replay retains at most 1000 events for 5 minutes. Last-Event-ID outside that window
         *     causes a reset event; refetch authoritative state and then continue listening.
         *     Heartbeat comments arrive every 15 seconds. Reconnect delay is 3 seconds. Events
         *     omit filenames and credentials. Revocation or session expiry closes the stream.
         *     Polling the corresponding resource remains supported.
         */
        get: operations["getEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/tokens": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listTokens"];
        put?: never;
        post: operations["createToken"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/tokens/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /** @description Revoke access, clear temporary credentials and cancel this token's active work. */
        delete: operations["revokeToken"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/preferences": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["getPreferences"];
        put: operations["setPreferences"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/bookmarks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get: operations["listBookmarks"];
        put?: never;
        post: operations["createBookmark"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/bookmarks/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        delete: operations["deleteBookmark"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        /** @enum {string} */
        ErrorCode: "invalid_request" | "unauthenticated" | "forbidden" | "csrf_failed" | "not_found" | "invalid_state" | "snapshot_expired" | "idempotency_conflict" | "restart_required" | "scan_superseded" | "request_too_large" | "listing_limit" | "unsupported_media" | "rate_limited" | "budget_exceeded" | "upstream_auth_required" | "upstream_unavailable" | "upstream_timeout" | "unsupported_listing" | "representation_changed" | "range_not_satisfiable" | "unsafe_path" | "unsafe_origin" | "credentials_required" | "unavailable" | "storage_full" | "internal_error";
        ApiError: {
            code: components["schemas"]["ErrorCode"];
            message: string;
            request_id: string;
        };
        Session: {
            /** @constant */
            authenticated: true;
            csrf_token: string;
            /** Format: date-time */
            expires_at: string;
        };
        Credentials: {
            username: string;
            password: string;
            /** @default false */
            remember: boolean;
        };
        ProfileCreate: {
            name: string;
            /** Format: uri */
            root_url: string;
            /**
             * @default none
             * @enum {string}
             */
            auth_type: "none" | "basic";
            credentials?: components["schemas"]["Credentials"];
            /** @default false */
            allow_insecure_http: boolean;
            /** @default true */
            previews_enabled: boolean;
            /**
             * @default 1
             * @enum {integer}
             */
            connections_per_file: 1 | 2 | 4;
            /** @default 0 */
            position: number;
        };
        ProfilePatch: {
            name?: string;
            /** Format: uri */
            root_url?: string;
            /** @enum {string} */
            auth_type?: "none" | "basic";
            /** @description Omit to keep credentials; null clears remembered and temporary credentials. */
            credentials?: components["schemas"]["Credentials"] | null;
            allow_insecure_http?: boolean;
            previews_enabled?: boolean;
            /** @enum {integer} */
            connections_per_file?: 1 | 2 | 4;
            position?: number;
        };
        Profile: {
            id: string;
            name: string;
            /** Format: uri */
            root_url: string;
            /** @enum {string} */
            auth_type: "none" | "basic";
            /** @enum {string} */
            credential_state: "not_required" | "locked" | "temporary" | "remembered";
            allow_insecure_http: boolean;
            previews_enabled: boolean;
            /** @enum {integer} */
            connections_per_file: 1 | 2 | 4;
            position: number;
        };
        UnlockRequest: {
            username: string;
            password: string;
            /** @default 3600 */
            lease_seconds: number;
        };
        ConnectionTest: {
            /** @enum {string} */
            status: "ready";
            /** @enum {string} */
            listing_format: "apache_table" | "apache_pre" | "nginx";
            /** Format: uri */
            canonical_root_url: string;
            /** @enum {string} */
            range_observation: "not_tested" | "supported_for_sample" | "unsupported_for_sample";
        };
        /** @enum {string} */
        Category: "folder" | "video" | "image" | "audio" | "document" | "archive" | "other";
        Entry: {
            /** @description Opaque profile-bound identity derived from the validated target, never the HTML label. */
            id: string;
            profile_id: string;
            parent_id: string;
            name: string;
            /** @description Safe escaped path relative to the configured upstream root; never a local path. */
            escaped_path: string;
            /** @enum {string} */
            kind: "file" | "directory";
            extension: string | null;
            category: components["schemas"]["Category"];
            size: number | null;
            /** @description False for rounded listing sizes and unknown sizes. */
            size_exact: boolean;
            /**
             * Format: date-time
             * @description Only populated when the time zone is known.
             */
            modified_at: string | null;
            /** @description Source timestamp text for uncertain listing times. */
            modified_text: string | null;
            timezone_known: boolean;
            /** @enum {string} */
            provenance: "apache_table" | "apache_pre" | "nginx" | "response_headers";
        };
        BrowsePage: {
            entries: components["schemas"]["Entry"][];
            snapshot_id: string;
            next_cursor: string | null;
            stale: boolean;
            /** Format: date-time */
            fetched_at: string;
            directory_id: string;
            parent_id: string | null;
            /** @description Number matching the current filters in this complete snapshot. */
            total_entries: number;
        };
        /** @enum {string} */
        JobState: "queued" | "running" | "paused" | "waiting_for_credentials" | "interrupted" | "completed" | "cancelled" | "failed";
        ScanCreate: {
            /** @default root */
            directory_id: string;
            /** @default 64 */
            max_depth: number;
            /** @default 100000 */
            max_entries: number;
            /** @default 1800 */
            max_seconds: number;
            /** @description Relative directory prefixes; entries are still validated against the selected root. */
            excluded_directories?: string[];
            /** @description Filters output files without suppressing traversal. */
            categories?: components["schemas"]["Category"][];
        };
        Coverage: {
            profile_id: string;
            directory_id: string;
            /** @enum {string} */
            state: "absent" | "partial" | "complete" | "stale";
            /** Format: date-time */
            last_scan_at: string | null;
        };
        Scan: {
            id: string;
            profile_id: string;
            directory_id: string;
            /** @enum {string} */
            state: "queued" | "running" | "paused" | "waiting_for_credentials" | "interrupted" | "completed" | "cancelled" | "failed" | "partial";
            directories_visited: number;
            files_found: number;
            directories_failed: number;
            directories_skipped: number;
            elapsed_seconds: number;
            coverage: components["schemas"]["Coverage"];
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
            error: components["schemas"]["ApiError"] | null;
        };
        SearchPage: {
            entries: components["schemas"]["Entry"][];
            next_cursor: string | null;
            coverage: components["schemas"]["Coverage"][];
        };
        Destination: {
            id: string;
            label: string;
            available_bytes: number | null;
            available: boolean;
            browseable: boolean;
        };
        DestinationFolders: {
            destination_id: string;
            path: string;
            folders: {
                name: string;
                path: string;
            }[];
        };
        DownloadCreate: {
            profile_id: string;
            /** @description One file ID. Directories require a separate scan and review. */
            entry_id: string;
            destination_id: string;
            /** @description Existing folder relative to an owner-configured destination. Only owner sessions may select mounted folders. */
            destination_path?: string;
            /**
             * @default 1
             * @enum {integer}
             */
            connections: 1 | 2 | 4;
            /** @default null */
            bytes_per_second: number | null;
        };
        Download: {
            id: string;
            /** @description Null after profile deletion; completed local files remain owner-only. */
            profile_id: string | null;
            entry_id: string;
            name: string;
            /** @description Final filename in the selected folder */
            saved_name: string | null;
            destination_id: string;
            destination_path: string;
            state: components["schemas"]["JobState"];
            transferred_bytes: number;
            /** @description Independently known exact total; unknown-length transfers keep null. */
            total_bytes: number | null;
            bytes_per_second: number | null;
            eta_seconds: number | null;
            connections: number;
            fallback_reason: string | null;
            file_available: boolean;
            /** Format: date-time */
            created_at: string;
            /** Format: date-time */
            updated_at: string;
            error: components["schemas"]["ApiError"] | null;
        };
        Thumbnail: {
            id: string;
            entry_id: string;
            /** @enum {string} */
            state: "pending" | "ready" | "unavailable" | "disabled";
        };
        Event: {
            /** @enum {string} */
            type: "scan" | "download" | "thumbnail" | "reset";
            object_id: string | null;
            profile_id: string | null;
            /** @description Resource state, or refetch_required for reset. */
            state: string;
        };
        /** @enum {string} */
        TokenScope: "browse:read" | "content:read" | "scan:write" | "downloads:write" | "credentials:unlock";
        TokenCreate: {
            name: string;
            profile_ids: string[];
            scopes: components["schemas"]["TokenScope"][];
            /** Format: date-time */
            expires_at?: string | null;
        };
        Token: {
            id: string;
            name: string;
            profile_ids: string[];
            scopes: components["schemas"]["TokenScope"][];
            /** Format: date-time */
            expires_at: string | null;
            /** Format: date-time */
            created_at: string;
        };
        Status: {
            image_thumbnails: boolean;
            /**
             * @description Ready means an authenticated worker polled within the last 15 seconds.
             * @enum {string}
             */
            video_worker: "disabled" | "ready" | "unavailable";
            cache_max_bytes: number;
            stream_max_bytes: number;
            server_downloads: boolean;
            segmented_downloads: boolean;
        };
        Preferences: {
            /** @enum {string} */
            theme: "system" | "light" | "dark";
            /**
             * @default amber
             * @enum {string}
             */
            accent: "blue" | "teal" | "violet" | "rose" | "amber" | "graphite";
            /**
             * @default comfortable
             * @enum {string}
             */
            density: "comfortable" | "compact";
            filter_presets: components["schemas"]["FilterPreset"][];
            /** @description Existing profile IDs in display order. Profiles omitted here follow in their usual order. Deleted profiles are removed on read and the next save. */
            profile_order: string[];
            last_destination?: components["schemas"]["DestinationFolder"];
            destination_favorites?: components["schemas"]["DestinationFolder"][];
            profile_views: {
                [key: string]: {
                    /** @enum {string} */
                    view: "detailed" | "compact" | "grid";
                    /** @enum {string} */
                    sort: "name" | "size" | "modified" | "type";
                    /** @enum {string} */
                    direction: "asc" | "desc";
                    folders_first: boolean;
                };
            };
        };
        /** @description The body is limited to 64 KiB. New fields may be omitted to preserve their stored values. Empty arrays clear saved presets or ordering. Explicit nulls and duplicate keys are rejected. */
        PreferencesUpdate: {
            theme: components["schemas"]["Preferences"]["theme"];
            profile_views: components["schemas"]["Preferences"]["profile_views"];
            accent?: components["schemas"]["Preferences"]["accent"];
            density?: components["schemas"]["Preferences"]["density"];
            filter_presets?: components["schemas"]["Preferences"]["filter_presets"];
            profile_order?: components["schemas"]["Preferences"]["profile_order"];
            last_destination?: components["schemas"]["DestinationFolder"];
            destination_favorites?: components["schemas"]["Preferences"]["destination_favorites"];
        };
        DestinationFolder: {
            destination_id: string;
            /** @description Slash-separated folder path relative to the configured destination. Empty means its root. */
            path: string;
        };
        FilterPreset: {
            id: string;
            name: string;
            /** @description Omit for a preset usable across profiles. A scoped preset is removed when its profile no longer exists. */
            profile_id?: string;
            filters: components["schemas"]["PresetFilters"];
        };
        /** @description Filters use the listing and indexed-search query vocabulary. Size and date bounds are inclusive. Omitted filters impose no constraint; omitted show_folders defaults to true. */
        PresetFilters: {
            /** @description Case-insensitive name query limited to 512 UTF-8 bytes. */
            name?: string;
            /** @enum {string} */
            category?: "" | "folder" | "video" | "image" | "audio" | "document" | "archive" | "other";
            /** @description Comma-separated extensions without dots. Limited to 512 UTF-8 bytes. */
            extensions?: string;
            /** Format: int64 */
            min_size?: number;
            /** Format: int64 */
            max_size?: number;
            /** Format: date-time */
            modified_after?: string;
            /** Format: date-time */
            modified_before?: string;
            show_folders?: boolean;
        };
        BookmarkCreate: {
            profile_id: string;
            directory_id: string;
            name: string;
            /** @default 0 */
            position: number;
        };
        Bookmark: {
            id: string;
            profile_id: string;
            directory_id: string;
            name: string;
            position: number;
        };
    };
    responses: {
        /**
         * @description Structured error. 400 invalid_request; 401 unauthenticated; 403 forbidden/csrf_failed;
         *     404 not_found; 409 invalid_state/snapshot_expired/idempotency_conflict/restart_required/scan_superseded;
         *     413 request_too_large/listing_limit; 415 unsupported_media; 429 rate_limited/budget_exceeded;
         *     502 upstream_auth_required/upstream_unavailable/unsupported_listing/representation_changed;
         *     503 unavailable; 507 storage_full. Other stable codes are listed in ErrorCode.
         */
        Error: {
            headers: {
                "X-Request-ID"?: string;
                "Retry-After"?: number;
                [name: string]: unknown;
            };
            content: {
                /**
                 * @example {
                 *       "code": "snapshot_expired",
                 *       "message": "This listing expired. Refresh the folder.",
                 *       "request_id": "request-example"
                 *     }
                 */
                "application/json": components["schemas"]["ApiError"];
            };
        };
        /** @description Active owner session. Response uses Cache-Control no-store. */
        Session: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Session"];
            };
        };
        /** @description Profile settings and credential state; no username or password. */
        Profile: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Profile"];
            };
        };
        /** @description Authoritative persisted scan state. */
        Scan: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Scan"];
            };
        };
        /** @description Authoritative persisted server download state. */
        Download: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Download"];
            };
        };
        /** @description Owner preferences. */
        Preferences: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Preferences"];
            };
        };
        /** @description File headers without a body. Unknown length is omitted. Upstream cookies/challenges are stripped. */
        ContentHeaders: {
            headers: {
                "Content-Length"?: number;
                "Content-Type"?: string;
                "Content-Disposition"?: string;
                ETag?: string;
                "Last-Modified"?: string;
                "Accept-Ranges"?: "bytes" | "none";
                "Content-Range"?: string;
                [name: string]: unknown;
            };
            content?: never;
        };
        /** @description Streamed file; Content-Disposition attachment unless passive preview was explicitly requested. */
        Content: {
            headers: {
                "Content-Length"?: number;
                "Content-Disposition"?: string;
                ETag?: string;
                "Accept-Ranges"?: "bytes" | "none";
                [name: string]: unknown;
            };
            content: {
                "application/octet-stream": string;
                "image/jpeg": string;
                "image/png": string;
                "image/webp": string;
                "audio/mpeg": string;
                "video/mp4": string;
                "video/webm": string;
            };
        };
        /** @description Verified single byte range from one representation. */
        PartialContent: {
            headers: {
                "Content-Range": string;
                "Content-Length": number;
                ETag?: string;
                [name: string]: unknown;
            };
            content: {
                "application/octet-stream": string;
            };
        };
        /** @description Requested range cannot be served. HEAD has no response body. */
        RangeError: {
            headers: {
                "Content-Range"?: string;
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ApiError"];
            };
        };
    };
    parameters: {
        ID: string;
        EntryID: string;
        Limit: number;
        Cursor: string;
        NameFilter: string;
        Category: components["schemas"]["Category"];
        /** @description Comma-separated case-insensitive extensions without the dot. */
        Extensions: string;
        MinSize: number;
        MaxSize: number;
        /** @description Unknown or timezone-uncertain timestamps do not match instant filters. */
        ModifiedAfter: string;
        ModifiedBefore: string;
        /** @description One bytes range; multiple ranges are rejected. */
        Range: string;
        IfRange: string;
        IfNoneMatch: string;
        IdempotencyKey: string;
    };
    requestBodies: {
        Login: {
            content: {
                "application/json": {
                    password: string;
                    /**
                     * @description Keep this device signed in with a renewable 90-day inactivity limit and a one-year maximum.
                     * @default false
                     */
                    remember_me?: boolean;
                };
            };
        };
        ProfileWrite: {
            content: {
                "application/json": components["schemas"]["ProfileCreate"];
            };
        };
    };
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getSetupStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Whether this fresh installation is ready to create its owner. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        setup_required: boolean;
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    setupOwner: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: components["requestBodies"]["Login"];
        responses: {
            200: components["responses"]["Session"];
            /** @description This installation has already been configured. */
            409: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    login: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: components["requestBodies"]["Login"];
        responses: {
            200: components["responses"]["Session"];
            default: components["responses"]["Error"];
        };
    };
    logout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Session and its temporary credentials were cleared. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    getSession: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Session"];
            default: components["responses"]["Error"];
        };
    };
    listProfiles: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Profiles visible to the caller, ordered by position then ID. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        profiles: components["schemas"]["Profile"][];
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    createProfile: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ProfileWrite"];
        responses: {
            201: components["responses"]["Profile"];
            default: components["responses"]["Error"];
        };
    };
    getProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Profile"];
            default: components["responses"]["Error"];
        };
    };
    deleteProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Profile removed. No upstream files were changed. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    updateProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProfilePatch"];
            };
        };
        responses: {
            200: components["responses"]["Profile"];
            default: components["responses"]["Error"];
        };
    };
    testProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Bounded listing check. Any range result is specific to the sampled file. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConnectionTest"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    unlockProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["UnlockRequest"];
            };
        };
        responses: {
            /** @description Lease created. No credential is returned. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        /** Format: date-time */
                        expires_at: string;
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    lockProfile: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Lease cleared. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    browse: {
        parameters: {
            query?: {
                directory_id?: string;
                limit?: components["parameters"]["Limit"];
                cursor?: components["parameters"]["Cursor"];
                name?: components["parameters"]["NameFilter"];
                category?: components["parameters"]["Category"];
                /** @description Comma-separated case-insensitive extensions without the dot. */
                extensions?: components["parameters"]["Extensions"];
                min_size?: components["parameters"]["MinSize"];
                max_size?: components["parameters"]["MaxSize"];
                /** @description Unknown or timezone-uncertain timestamps do not match instant filters. */
                modified_after?: components["parameters"]["ModifiedAfter"];
                modified_before?: components["parameters"]["ModifiedBefore"];
                show_folders?: boolean;
                sort?: "name" | "size" | "modified" | "type";
                direction?: "asc" | "desc";
                folders_first?: boolean;
                refresh?: boolean;
            };
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description A bounded page from one listing snapshot. Filtering applies only to this directory. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BrowsePage"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getEntry: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
                entry_id: components["parameters"]["EntryID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Entry metadata after checking profile ownership. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Entry"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getEntryContent: {
        parameters: {
            query?: {
                /** @description Preview accepts only passive allowlisted media. HTML and SVG are never inline app-origin content. */
                disposition?: "attachment" | "preview";
            };
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
                "If-None-Match"?: components["parameters"]["IfNoneMatch"];
            };
            path: {
                id: components["parameters"]["ID"];
                entry_id: components["parameters"]["EntryID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Content"];
            206: components["responses"]["PartialContent"];
            /** @description Representation unchanged. */
            304: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            416: components["responses"]["RangeError"];
            default: components["responses"]["Error"];
        };
    };
    headEntryContent: {
        parameters: {
            query?: {
                /** @description Preview accepts only passive allowlisted media. HTML and SVG are never inline app-origin content. */
                disposition?: "attachment" | "preview";
            };
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
                "If-None-Match"?: components["parameters"]["IfNoneMatch"];
            };
            path: {
                id: components["parameters"]["ID"];
                entry_id: components["parameters"]["EntryID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["ContentHeaders"];
            206: components["responses"]["ContentHeaders"];
            /** @description Representation unchanged. */
            304: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            416: components["responses"]["RangeError"];
            default: components["responses"]["Error"];
        };
    };
    createScan: {
        parameters: {
            query?: never;
            header: {
                "Idempotency-Key": components["parameters"]["IdempotencyKey"];
            };
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ScanCreate"];
            };
        };
        responses: {
            202: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    listScans: {
        parameters: {
            query?: {
                limit?: components["parameters"]["Limit"];
                cursor?: components["parameters"]["Cursor"];
                profile_id?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Visible scans, newest created_at then ID first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        scans: components["schemas"]["Scan"][];
                        next_cursor: string | null;
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getScan: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    pauseScan: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    resumeScan: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    cancelScan: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    retryScan: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Scan"];
            default: components["responses"]["Error"];
        };
    };
    exportScan: {
        parameters: {
            query?: {
                format?: "json" | "csv";
            };
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Streamed scan results including coverage. CSV cells escape spreadsheet formulas. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        coverage: components["schemas"]["Coverage"];
                        entries: components["schemas"]["Entry"][];
                    };
                    "text/csv": string;
                };
            };
            default: components["responses"]["Error"];
        };
    };
    searchIndex: {
        parameters: {
            query?: {
                q?: string;
                match?: "substring" | "prefix";
                profile_id?: string;
                /** @description Requires profile_id. */
                directory_id?: string;
                /** @description Return direct children of directory_id only. */
                direct_only?: true;
                /** @description Comma-separated file types. Cannot be combined with category. */
                categories?: string;
                limit?: components["parameters"]["Limit"];
                cursor?: components["parameters"]["Cursor"];
                category?: components["parameters"]["Category"];
                /** @description Comma-separated case-insensitive extensions without the dot. */
                extensions?: components["parameters"]["Extensions"];
                min_size?: components["parameters"]["MinSize"];
                max_size?: components["parameters"]["MaxSize"];
                /** @description Unknown or timezone-uncertain timestamps do not match instant filters. */
                modified_after?: components["parameters"]["ModifiedAfter"];
                modified_before?: components["parameters"]["ModifiedBefore"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Results ordered by natural name then profile ID and entry ID; cursor binds to index generation. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SearchPage"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    listDestinations: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Configured server storage labels. Local filesystem paths are not returned. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        destinations: components["schemas"]["Destination"][];
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    logoutAllDevices: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description All owner sessions were revoked */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    refreshSession: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Session"];
            default: components["responses"]["Error"];
        };
    };
    listDestinationFolders: {
        parameters: {
            query?: {
                /** @description Slash-separated folder path relative to the configured destination. */
                path?: string;
            };
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Existing folders inside one configured mount. Symlinks are not traversed. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DestinationFolders"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    listDownloads: {
        parameters: {
            query?: {
                limit?: components["parameters"]["Limit"];
                cursor?: components["parameters"]["Cursor"];
                state?: components["schemas"]["JobState"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Owner sees all jobs; tokens see only their own jobs. Newest created_at then ID first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        downloads: components["schemas"]["Download"][];
                        next_cursor: string | null;
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    createDownload: {
        parameters: {
            query?: never;
            header: {
                "Idempotency-Key": components["parameters"]["IdempotencyKey"];
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["DownloadCreate"];
            };
        };
        responses: {
            202: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    getDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    pauseDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    resumeDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "application/json": {
                    /** @default false */
                    allow_restart?: boolean;
                };
            };
        };
        responses: {
            202: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    cancelDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    retryDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["Download"];
            default: components["responses"]["Error"];
        };
    };
    deletePartialDownload: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Partial output removed. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    getDownloadContent: {
        parameters: {
            query?: never;
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
            };
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Content"];
            206: components["responses"]["PartialContent"];
            416: components["responses"]["RangeError"];
            default: components["responses"]["Error"];
        };
    };
    deleteDownloadedFile: {
        parameters: {
            query?: never;
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
            };
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Local file deleted. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    headDownloadContent: {
        parameters: {
            query?: never;
            header?: {
                /** @description One bytes range; multiple ranges are rejected. */
                Range?: components["parameters"]["Range"];
                "If-Range"?: components["parameters"]["IfRange"];
            };
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["ContentHeaders"];
            206: components["responses"]["ContentHeaders"];
            416: components["responses"]["RangeError"];
            default: components["responses"]["Error"];
        };
    };
    requestThumbnails: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    entry_ids: string[];
                };
            };
        };
        responses: {
            /** @description Existing thumbnail IDs or accepted bounded work for visible entries. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        thumbnails: components["schemas"]["Thumbnail"][];
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getThumbnail: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Already generated thumbnail after profile authorization; no new upstream request. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "image/jpeg": string;
                    "image/webp": string;
                };
            };
            /** @description Thumbnail is pending. Obey Retry-After. */
            202: {
                headers: {
                    "Retry-After"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Thumbnail"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getEvents: {
        parameters: {
            query?: never;
            header?: {
                "Last-Event-ID"?: string;
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Unbuffered SSE stream with Cache-Control no-store. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "text/event-stream": string;
                };
            };
            default: components["responses"]["Error"];
        };
    };
    listTokens: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Token metadata without secrets or secret hashes. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        tokens: components["schemas"]["Token"][];
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    createToken: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TokenCreate"];
            };
        };
        responses: {
            /** @description Secret is shown once. The response uses Cache-Control no-store. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        token: components["schemas"]["Token"];
                        secret: string;
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    revokeToken: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Token revoked. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
    getStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Configured capabilities and current video worker availability. No private paths or source URLs. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Status"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    getPreferences: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            200: components["responses"]["Preferences"];
            default: components["responses"]["Error"];
        };
    };
    setPreferences: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PreferencesUpdate"];
            };
        };
        responses: {
            200: components["responses"]["Preferences"];
            default: components["responses"]["Error"];
        };
    };
    listBookmarks: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Owner's pinned folders, ordered by position then ID. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        bookmarks: components["schemas"]["Bookmark"][];
                    };
                };
            };
            default: components["responses"]["Error"];
        };
    };
    createBookmark: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["BookmarkCreate"];
            };
        };
        responses: {
            /** @description Folder pinned. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Bookmark"];
                };
            };
            default: components["responses"]["Error"];
        };
    };
    deleteBookmark: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["ID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Bookmark removed. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            default: components["responses"]["Error"];
        };
    };
}
