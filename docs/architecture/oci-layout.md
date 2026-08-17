# Architecture and OCI layout

The CLI validates an executable, creates a deterministic `tar` layer and
gzip stream, then writes the OCI image configuration, manifest, index,
and content-addressed blobs. A generated layout has this shape:

```text
oci-image/
  oci-layout                 # imageLayoutVersion 1.0.0
  index.json                 # platform descriptor and image reference annotation
  blobs/sha256/
    <config digest>
    <manifest digest>
    <compressed layer digest>
```

The layer contains `/app/service`, `/etc/ssl/certs/`, `/tmp/`, and
`/var/tmp/`, plus one entry per `-extra-file` if any were given. The last
two directories are sticky writable directories; all other included
directories are `0755`, and every file (the entrypoint and every extra
file) is `0555`. The config specifies `linux`, `amd64` or `arm64`, a
single compressed layer, its uncompressed `diff_id`, and `User`
`65532:65532`.

See also [`internal/layout`](../../internal/layout) for the verifier that
reads this shape back, and the
[compatibility matrix](../reference/compatibility.md) for which
OS/architecture/runtime combinations actually consume it in CI.
