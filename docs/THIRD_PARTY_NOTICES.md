# Third-party notices

Folderhug's original source is covered by the repository's [MIT license](../LICENSE). Dependencies keep their own copyright notices and license terms. [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt) contains the license texts copied from the dependency versions below, including the additional notices shipped with modernc libraries.

## Native app and worker

The inventory below follows the modules required by `go.mod`. Which packages enter a binary depends on the target platform and build. The Go 1.27.1 standard-library license is included in the appendix as well.

| Module | Version | Main license |
| --- | --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT |
| `github.com/ncruces/go-strftime` | `v1.0.0` | MIT |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause |
| `golang.org/x/crypto` | `v0.57.0` | BSD-3-Clause |
| `golang.org/x/net` | `v0.59.0` | BSD-3-Clause |
| `golang.org/x/sys` | `v0.48.0` | BSD-3-Clause |
| `golang.org/x/term` | `v0.46.0` | BSD-3-Clause |
| `golang.org/x/text` | `v0.42.0` | BSD-3-Clause |
| `modernc.org/libc` | `v1.75.7` | BSD-3-Clause, with additional bundled notices |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause |
| `modernc.org/memory` | `v1.12.1` | BSD-3-Clause, with additional bundled notices |
| `modernc.org/sqlite` | `v1.59.0` | BSD-3-Clause; SQLite public-domain notice and sqlite-vec MIT notice included |

The `modernc.org/memory` appendix also retains its upstream logo notice for completeness. Folderhug does not use that logo.

## Browser runtime and build tools

The runtime notices cover Svelte 5.57.1, clsx 2.1.1, esm-env 1.2.2, `@tabler/icons-svelte-runes` 3.48.0, and `@tabler/icons` 3.48.0, all MIT licensed. The interface uses Tabler icons and system fonts; no external font service is required.

The Folderhug wordmark uses outlined Nunito ExtraBold shapes. Nunito is copyright 2014 The Nunito Project Authors and licensed under the SIL Open Font License 1.1. The license is included with the [served branding assets](../web/public/brand/FONT-LICENSE.txt). The app does not load a font file for the wordmark.

The lockfile also records development tools, including Vite, TypeScript, ESLint, Vitest, Playwright, and the OpenAPI generator. Their packages and downloaded browser builds retain their own licenses. `web/package-lock.json` identifies the npm package versions and license metadata. Development tools are not installed in the final app image. A release audit must still check the actual built browser bundle and the contents of each binary image rather than treating this source inventory as a complete image bill of materials.

## Container packages and FFmpeg

The app image includes Debian runtime packages such as the CA certificate bundle. Their notices are retained under `/usr/share/doc/` in the image. The optional media image installs Debian bookworm's `ffmpeg=7:5.1.9-0+deb12u1`; its observed configuration enables GPL components, including libx264. The Go worker invokes FFmpeg as a separate process and does not link its libraries. This does not change the licenses or distribution obligations of FFmpeg and its dependencies.

The media image retains Debian copyright files, `/usr/share/http-wrapper/ffmpeg-build.txt`, and `/usr/share/http-wrapper/packages.txt`. The base image and FFmpeg version are pinned; other Debian packages are resolved when the image is built. See [MEDIA_WORKER.md](MEDIA_WORKER.md) for the package selection and source links.

Before publishing a binary or container image, preserve the notices with the artifact, identify the exact installed packages, and provide the corresponding source and license material required for those distributed packages. The current source Dockerfile and links to Debian packages do not by themselves complete that distribution work. No public image has been published.

## Publication items still pending

- The source repository is [themartz90/folderhug](https://github.com/themartz90/folderhug). The Go module uses that path.
- Enable GitHub private vulnerability reporting when the repository becomes public, and document the supported release policy.
- Select release artifacts and any container registry names, then record their exact dependency and package inventories.
- Package the matching third-party notices and FFmpeg/dependency source material required for each distributed binary image.

The license appendix is a local source inventory, not a claim that these publication items have passed review.
