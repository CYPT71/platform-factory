// Reference platform-factory v1 language plugin, built on the SecureOci.Plugin
// SDK. This is the C# counterpart of examples/sdk/plugin-go: where the Go
// example implements sdk/plugin.LanguageExtension and lets
// sdk/plugin.Runtime handle framing, handshake and dispatch, this example
// references SecureOci.Plugin's Server and does the same. Both speak the
// exact same wire protocol and pass the exact same conformance suite
// (platform-factory-conformance plugin).
using SecureOci.Plugin;

var server = new Server("csharp-example", "1.0.0");

server.Handle("detect", _ => new { kind = "csharp", profile = "dotnet", evidence = new[] { "csharp-example" } });
server.Handle("freeze", _ => new { steps = new[] { new[] { "dotnet", "restore", "--locked-mode" } }, profile = "dotnet" });
server.Handle("plan", _ => new { notes = new[] { "C# example extension selected" } });

server.Serve(Console.OpenStandardInput(), Console.OpenStandardOutput());
