# Local-dev-only variant of the repository root Dockerfile: one Alpine
# builder stage instead of two, same checks. Build context: ./oci-image.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS builder
RUN apk add --no-cache jq tar gzip
WORKDIR /layout
COPY oci-image/ ./
# Validate the required OCI layout files and every content-addressed blob.
RUN test -f oci-layout && test -f index.json && \
    test "$(find blobs/sha256 -type f | wc -l)" -eq 3 && \
    for blob in blobs/sha256/*; do test "$(sha256sum "$blob" | cut -d' ' -f1)" = "$(basename "$blob")"; done
# Extracts the sole layer and symlinks /entrypoint to whatever path the
# config actually declares - avoids hardcoding /app/service.
RUN manifest=$(jq -er '.manifests | if length == 1 then .[0].digest else error("expected one manifest") end | select(startswith("sha256:")) | ltrimstr("sha256:")' index.json) && \
    test -f "blobs/sha256/$manifest" && \
    config=$(jq -er '.config.digest | select(startswith("sha256:")) | ltrimstr("sha256:")' "blobs/sha256/$manifest") && \
    test -f "blobs/sha256/$config" && \
    entrypoint=$(jq -er '.config.Entrypoint | if length == 1 then .[0] else error("expected one entrypoint") end' "blobs/sha256/$config") && \
    layer=$(jq -er '.layers | if length == 1 then .[0].digest else error("expected one layer") end | select(startswith("sha256:")) | ltrimstr("sha256:")' "blobs/sha256/$manifest") && \
    test -f "blobs/sha256/$layer" && \
    mkdir /rootfs && gzip -dc "blobs/sha256/$layer" | tar -x --no-same-owner --no-same-permissions -C /rootfs && \
    ln -s "$entrypoint" /rootfs/entrypoint

FROM scratch
COPY --from=builder /rootfs/ /
USER 65532:65532
ENTRYPOINT ["/entrypoint"]
