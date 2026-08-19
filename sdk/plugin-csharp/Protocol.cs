// SDK for out-of-process secure-oci language plugins. Defines the
// versioned, length-prefixed JSON-RPC protocol plugins speak over
// stdin/stdout (an LSP/DAP-style header-framed message on the wire) and
// the plugin-side Server. Mirrors Go's sdk/plugin package: the same wire
// protocol, the same v1.hello handshake, the same capability dispatch
// (method "v1."+capability). A plugin written against either SDK passes
// the exact same conformance suite (secure-oci-conformance plugin).
//
// No third-party dependencies - System.Text.Json only.
using System.Text;
using System.Text.Json;

namespace SecureOci.Plugin;

public readonly record struct RequestContext(string TraceId, string OperationId);

public static class Capabilities
{
    public const string RuntimeStatus = "runtime.status", RuntimeLogs = "runtime.logs", RuntimeCreate = "runtime.create", RuntimeStart = "runtime.start", RuntimeStop = "runtime.stop", RuntimeRestart = "runtime.restart", RuntimeDelete = "runtime.delete", RuntimeExec = "runtime.exec";
    public const string DeploymentPlan = "deployment.plan", DeploymentApply = "deployment.apply", DeploymentObserve = "deployment.observe", DeploymentRollback = "deployment.rollback", DeploymentDelete = "deployment.delete";
    public const string BuilderBuild = "builder.build", AnalyzerScan = "analyzer.scan", RegistryPush = "registry.push", MigrationDiscover = "migration.discover", MigrationApply = "migration.apply";
}

/// <summary>A protocol-level error a handler can throw to control the
/// exact code/message sent back to the host, instead of the generic 500
/// an unhandled exception produces.</summary>
public sealed class RpcException : Exception
{
    public int Code { get; }

    public RpcException(int code, string message) : base(message)
    {
        Code = code;
    }
}

/// <summary>ProtocolException reports a framing or transport-level
/// error: malformed headers, an unsupported Content-Type, a truncated
/// body, or a body over the 1 MiB limit.</summary>
public sealed class ProtocolException : Exception
{
    public ProtocolException(string message) : base(message) { }
}

/// <summary>Server is the plugin-side SDK: register capabilities, then
/// call Serve.</summary>
public sealed class Server
{
    public const string ContentType = "application/vnd.platform-factory.rpc.v1+json";

    /// <summary>LegacyContentType is the pre-rebrand Content-Type: still
    /// accepted from a peer for the documented compatibility overlap
    /// window (see docs/api-compatibility.md), never written.</summary>
    public const string LegacyContentType = "application/vnd.secure-oci.rpc.v1+json";
    public const string ProtocolVersion = "v1";
    private const int MaxMessageBytes = 1 << 20;

    private readonly string _name;
    private readonly string _version;
    private readonly List<string> _capabilities = new();
    private readonly Dictionary<string, Func<JsonElement, object>> _handlers = new();
    private readonly Dictionary<string, Func<JsonElement, RequestContext, object>> _contextHandlers = new();

    public Server(string name, string version)
    {
        if (string.IsNullOrEmpty(name) || string.IsNullOrEmpty(version))
        {
            throw new ArgumentException("SecureOci.Plugin.Server requires a name and a version");
        }
        _name = name;
        _version = version;
    }

    /// <summary>Handle registers handler for capability (e.g. "detect"),
    /// dispatched on method "v1."+capability and advertised in the
    /// v1.hello response.</summary>
    public Server Handle(string capability, Func<JsonElement, object> handler)
    {
        _capabilities.Add(capability);
        _handlers["v1." + capability] = handler;
        return this;
    }

    public Server Handle(string capability, Func<JsonElement, RequestContext, object> handler)
    {
        _capabilities.Add(capability);
        _contextHandlers["v1." + capability] = handler;
        return this;
    }

    /// <summary>Serve reads framed requests from input and writes
    /// framed responses to output until input is exhausted (the host
    /// closed the connection).</summary>
    public void Serve(Stream input, Stream output)
    {
        while (true)
        {
            var raw = ReadMessage(input);
            if (raw is null)
            {
                return;
            }
            var response = Dispatch(raw.Value);
            WriteMessage(output, response);
        }
    }

    private object Dispatch(JsonElement request)
    {
        var id = request.TryGetProperty("id", out var idElement) ? idElement.GetString() ?? "" : "";
        var method = request.TryGetProperty("method", out var methodElement) ? methodElement.GetString() : null;
        var hasParams = request.TryGetProperty("params", out var paramsElement);
		var traceId = request.TryGetProperty("trace_id", out var requestTrace) ? requestTrace.GetString() ?? "" : "";
		var operationId = request.TryGetProperty("operation_id", out var requestOperation) ? requestOperation.GetString() ?? "" : "";

        if (method == "v1.hello")
        {
            return new
            {
                id,
                result = new
                {
                    api_version = ProtocolVersion,
                    name = _name,
                    version = _version,
                    capabilities = _capabilities,
				}, trace_id = traceId, operation_id = operationId,
            };
        }

		Func<JsonElement, object>? handler = null;
		Func<JsonElement, RequestContext, object>? contextHandler = null;
		var hasHandler = method is not null && _handlers.TryGetValue(method, out handler);
		var hasContextHandler = method is not null && _contextHandlers.TryGetValue(method, out contextHandler);
        if (!hasHandler && !hasContextHandler)
        {
            return new { id, error = new { code = 404, message = $"unknown method \"{method}\"" }, trace_id = traceId, operation_id = operationId };
        }
        try
        {
			var context = new RequestContext(traceId, operationId);
            var parameters = hasParams ? paramsElement : default;
            var result = hasContextHandler ? contextHandler!(parameters, context) : handler!(parameters);
            return new { id, result, trace_id = context.TraceId, operation_id = context.OperationId };
        }
        catch (RpcException exception)
        {
            return new { id, error = new { code = exception.Code, message = exception.Message }, trace_id = traceId, operation_id = operationId };
        }
        catch (Exception exception)
        {
            return new { id, error = new { code = 500, message = exception.Message }, trace_id = traceId, operation_id = operationId };
        }
    }

    /// <summary>WriteMessage frames value as a Content-Type/Content-Length-
    /// prefixed JSON message and writes it to output, matching Go's
    /// WriteMessage and Python's write_message byte for byte.</summary>
    public static void WriteMessage(Stream output, object value)
    {
        var body = JsonSerializer.SerializeToUtf8Bytes(value);
        if (body.Length > MaxMessageBytes)
        {
            throw new ProtocolException($"message of {body.Length} bytes exceeds the {MaxMessageBytes} byte limit");
        }
        var header = Encoding.ASCII.GetBytes($"Content-Type: {ContentType}\r\nContent-Length: {body.Length}\r\n\r\n");
        output.Write(header);
        output.Write(body);
        output.Flush();
    }

    /// <summary>ReadMessage reads one framed message from input and
    /// returns its decoded JSON body, or null at a clean EOF between
    /// messages.</summary>
    public static JsonElement? ReadMessage(Stream input)
    {
        var headers = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        string? line;
        var sawAnyLine = false;
        while ((line = ReadHeaderLine(input)) is not null && line.Length != 0)
        {
            sawAnyLine = true;
            var separator = line.IndexOf(':');
            if (separator < 1)
            {
                throw new ProtocolException($"malformed header \"{line}\"");
            }
            headers[line[..separator].Trim()] = line[(separator + 1)..].Trim();
        }
        if (line is null)
        {
            if (sawAnyLine)
            {
                throw new ProtocolException("truncated header");
            }
            return null;
        }

        if (!headers.TryGetValue("Content-Type", out var contentType) || (contentType != ContentType && contentType != LegacyContentType))
        {
            throw new ProtocolException($"unsupported Content-Type \"{contentType}\", want \"{ContentType}\"");
        }
        if (!headers.TryGetValue("Content-Length", out var rawLength) ||
            !int.TryParse(rawLength, out var length) || length < 0 || length > MaxMessageBytes)
        {
            throw new ProtocolException($"invalid Content-Length \"{rawLength}\"");
        }

        var body = new byte[length];
        var read = 0;
        while (read < length)
        {
            var n = input.Read(body, read, length - read);
            if (n == 0)
            {
                throw new ProtocolException("truncated body");
            }
            read += n;
        }
        using var document = JsonDocument.Parse(body);
        return document.RootElement.Clone();
    }

    private static string? ReadHeaderLine(Stream stream)
    {
        var bytes = new List<byte>();
        while (true)
        {
            var value = stream.ReadByte();
            if (value < 0)
            {
                return bytes.Count == 0 ? null : Encoding.ASCII.GetString(bytes.ToArray());
            }
            if (value == '\n')
            {
                return Encoding.ASCII.GetString(bytes.ToArray()).TrimEnd('\r');
            }
            bytes.Add((byte)value);
        }
    }
}
