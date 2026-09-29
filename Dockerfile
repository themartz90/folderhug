FROM node:24.21.0-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6 AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci
COPY web ./web
COPY api ./api
RUN cd web && npm run build

FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/http-wrapper ./cmd/http-wrapper
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/media-worker ./cmd/media-worker

FROM build AS test
COPY tests ./tests
CMD ["go", "test", "-race", "./..."]

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS app
ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/themartz90/folderhug" \
      org.opencontainers.image.title="Folderhug" \
      org.opencontainers.image.description="A self-hosted file explorer for HTTP directory listings" \
      org.opencontainers.image.revision=$VCS_REF
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 app && useradd --uid 10001 --gid app --no-create-home app \
    && mkdir /data /keys /downloads /cache /broker /worker-key && chown app:app /data /keys /downloads /cache /broker /worker-key
COPY --from=build /out/http-wrapper /usr/local/bin/http-wrapper
COPY LICENSE docs/THIRD_PARTY_NOTICES.md docs/THIRD_PARTY_LICENSES.txt /usr/share/doc/folderhug/
COPY web/public/brand/FONT-LICENSE.txt /usr/share/doc/folderhug/FONT-LICENSE.txt
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["http-wrapper"]
CMD ["serve"]

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS media
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg=7:5.1.9-0+deb12u1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 app && useradd --uid 10001 --gid app --no-create-home app \
    && mkdir /broker /worker-key /usr/share/http-wrapper \
    && chown app:app /broker /worker-key \
    && ffmpeg -buildconf > /usr/share/http-wrapper/ffmpeg-build.txt 2>&1 \
    && dpkg-query -W > /usr/share/http-wrapper/packages.txt
COPY --from=build /out/media-worker /usr/local/bin/media-worker
COPY LICENSE docs/THIRD_PARTY_NOTICES.md docs/THIRD_PARTY_LICENSES.txt /usr/share/doc/folderhug/
USER 10001:10001
ENTRYPOINT ["media-worker"]

FROM test AS media-test-build
RUN CGO_ENABLED=0 go test -c -o /out/mediaworker.test ./internal/mediaworker \
    && CGO_ENABLED=0 go test -c -o /out/mediabroker.test ./internal/mediabroker \
    && CGO_ENABLED=0 go test -c -o /out/thumbnails.test ./internal/thumbnails
FROM media AS media-test
COPY --from=media-test-build /out/mediaworker.test /out/mediabroker.test /out/thumbnails.test /usr/local/bin/
COPY scripts/generate-media-fixtures.sh /usr/local/bin/generate-media-fixtures
