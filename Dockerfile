FROM node:24.21.0-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6 AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci
COPY web ./web
COPY api ./api
RUN cd web && npm run build

FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG VERSION=dev
ARG VCS_REF=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./VERSION
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN if [ "$VERSION" != dev ]; then test "$VERSION" = "$(cat VERSION)"; fi \
    && CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/themartz90/folderhug/internal/buildinfo.Version=$VERSION -X github.com/themartz90/folderhug/internal/buildinfo.Revision=$VCS_REF" \
      -o /out/folderhug ./cmd/folderhug

FROM build AS test
COPY tests ./tests
CMD ["go", "test", "-race", "./..."]

FROM debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587 AS app
ARG VCS_REF=unknown
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/themartz90/folderhug" \
      org.opencontainers.image.title="Folderhug" \
      org.opencontainers.image.description="A self-hosted file explorer for HTTP directory listings" \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.version=$VERSION
RUN apt-get update && apt-get upgrade -y --no-install-recommends \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 app && useradd --uid 10001 --gid app --no-create-home app \
    && mkdir /data /keys /downloads /cache && chown app:app /data /keys /downloads /cache
COPY --from=build /out/folderhug /usr/local/bin/folderhug
RUN ln -s folderhug /usr/local/bin/http-wrapper
COPY LICENSE docs/THIRD_PARTY_NOTICES.md docs/THIRD_PARTY_LICENSES.txt /usr/share/doc/folderhug/
COPY web/public/brand/FONT-LICENSE.txt /usr/share/doc/folderhug/FONT-LICENSE.txt
ENV GOMEMLIMIT=384MiB \
    FOLDERHUG_CONTAINER=1
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["folderhug"]
CMD ["serve"]
