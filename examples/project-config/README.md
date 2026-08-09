# Project configuration

Inspect the normalized plan without executing a package manager or runtime:

```sh
platform-factory project show --config examples/project-config/.config_image.yaml examples/project-config
platform-factory plan --config examples/project-config/.config_image.yaml examples/project-config
```

`app.py` is a minimal HTTP service and `Shared_deps/` demonstrates an explicit
shared dependency mapping. `freeze`, `build`, `run` or `launch` may create the
declared `.platform-factory/deps/python` environment; `show` and `plan` remain
read-only.
