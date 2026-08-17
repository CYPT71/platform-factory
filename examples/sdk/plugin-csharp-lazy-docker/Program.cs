using System.Diagnostics;
using System.Text.Json;
using SecureOci.Plugin;

static string Engine(JsonElement parameters)
{
    var requested = parameters.ValueKind == JsonValueKind.Object && parameters.TryGetProperty("engine", out var value) ? value.GetString() ?? "auto" : "auto";
    if (requested is not ("auto" or "docker" or "podman")) throw new RpcException(400, "invalid engine");
    foreach (var candidate in requested == "auto" ? new[] { "docker", "podman" } : new[] { requested })
    {
        try { using var probe = Process.Start(new ProcessStartInfo(candidate, "version") { RedirectStandardOutput = true, RedirectStandardError = true }); if (probe is not null && probe.WaitForExit(5000) && probe.ExitCode == 0) return candidate; } catch { }
    }
    throw new RpcException(503, "Docker or Podman is unavailable");
}

static object Status(JsonElement parameters)
{
    var engine = Engine(parameters);
    using var process = Process.Start(new ProcessStartInfo(engine, "ps --all --format {{json .}}") { RedirectStandardOutput = true, RedirectStandardError = true }) ?? throw new RpcException(502, "cannot start engine");
    var output = process.StandardOutput.ReadToEnd(); process.WaitForExit(10000);
    if (process.ExitCode != 0) throw new RpcException(502, process.StandardError.ReadToEnd());
    var containers = output.Split('\n', StringSplitOptions.RemoveEmptyEntries).Select(line => JsonSerializer.Deserialize<Dictionary<string, JsonElement>>(line)!).Select(row => new {
        id = row.GetValueOrDefault("ID").ToString(), name = (row.GetValueOrDefault("Names").ToString() is var names && names != "" ? names : row.GetValueOrDefault("Name").ToString()), image = row.GetValueOrDefault("Image").ToString(), state = row.GetValueOrDefault("State").ToString(), ports = row.GetValueOrDefault("Ports").ToString()
    }).OrderBy(item => item.name).ToArray();
    return new { engine, containers };
}

var server = new Server("lazy-docker-csharp", "1.0.0");
server.Handle("detect", _ => new { kind = "unknown" });
server.Handle("freeze", _ => new { steps = new[] { new[] { "dotnet", "--version" } }, profile = "runtime-monitor" });
server.Handle("plan", _ => new { notes = new[] { "read-only Docker/Podman monitoring" } });
server.Handle(Capabilities.RuntimeStatus, (parameters, context) => new { status = Status(parameters), trace_id = context.TraceId });
server.Handle(Capabilities.RuntimeLogs, (parameters, context) => {
    var name = parameters.TryGetProperty("name", out var nameValue) ? nameValue.GetString() : null;
    var tail = parameters.TryGetProperty("tail", out var tailValue) ? tailValue.GetInt32() : 50;
    if (string.IsNullOrEmpty(name) || name.Contains('\0')) throw new RpcException(400, "name is required");
    if (tail is < 1 or > 500) throw new RpcException(400, "tail must be between 1 and 500");
    var engine = Engine(parameters); using var process = Process.Start(new ProcessStartInfo(engine, $"logs --tail {tail} {name}") { RedirectStandardOutput = true, RedirectStandardError = true }) ?? throw new RpcException(502, "cannot start engine");
    var output = process.StandardOutput.ReadToEnd(); process.WaitForExit(10000); if (process.ExitCode != 0) throw new RpcException(502, process.StandardError.ReadToEnd()); return new { engine, name, lines = output.TrimEnd('\n').Split('\n'), operation_id = context.OperationId };
});
server.Serve(Console.OpenStandardInput(), Console.OpenStandardOutput());
