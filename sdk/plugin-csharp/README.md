# SecureOci.Plugin

The .NET SDK for out-of-process secure-oci language plugins. It defines
the versioned, length-prefixed JSON-RPC protocol plugins speak over
stdin/stdout (an LSP/DAP-style header-framed message on the wire) and the
plugin-side `Server`. This mirrors Go's `sdk/plugin` package field for
field; a plugin written against either SDK speaks the exact same wire
protocol and passes the exact same conformance suite
(`platform-factory plugin`).

`System.Text.Json` only - no third-party dependencies.

## Usage

Reference this project from your plugin's `.csproj`:

```xml
<ItemGroup>
  <ProjectReference Include="path/to/platform-factory/sdk/plugin-csharp/SecureOci.Plugin.csproj" />
</ItemGroup>
```

```csharp
using SecureOci.Plugin;

var server = new Server("my-language", "1.0.0");

server.Handle("detect", _ => new { kind = "my-language", profile = "static" });
server.Handle("freeze", _ => new { steps = new[] { new[] { "my-package-manager", "freeze" } }, profile = "static" });
server.Handle("plan", _ => new { notes = new[] { "my-language extension selected" } });

server.Serve(Console.OpenStandardInput(), Console.OpenStandardOutput());
```

See `examples/sdk/plugin-csharp` for a complete, runnable example.

The contextual `Handle` overload accepts `(JsonElement, RequestContext)` for
any `Capabilities.*` value; the SDK owns framing and correlation echo.
