import importlib.util
import pathlib
import subprocess
import unittest
from unittest import mock

PATH = pathlib.Path(__file__).with_name("plugin.py")
SPEC = importlib.util.spec_from_file_location("lazy_docker_sdk_example", PATH)
PLUGIN = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PLUGIN)
from secure_oci_plugin import read_message, write_message  # noqa: E402


class LazyDockerSDKExampleTests(unittest.TestCase):
    def test_external_sdk_handshake(self):
        process = subprocess.Popen(
            [str(PATH)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE
        )
        try:
            write_message(process.stdin, {"id": "hello", "method": "v1.hello"})
            response = read_message(process.stdout)
            self.assertEqual(response["id"], "hello")
            self.assertEqual(response["result"]["api_version"], "v1")
            self.assertIn("runtime.status", response["result"]["capabilities"])
        finally:
            process.stdin.close()
            process.wait(timeout=5)
            process.stdout.close()
            process.stderr.close()

    def test_capabilities_are_registered_by_the_sdk(self):
        response = PLUGIN.server._dispatch({"id": "1", "method": "v1.hello"})
        capabilities = response["result"]["capabilities"]
        self.assertIn("runtime.status", capabilities)
        self.assertIn("runtime.logs", capabilities)

    @mock.patch.object(PLUGIN.shutil, "which", return_value="/usr/bin/podman")
    @mock.patch.object(PLUGIN.subprocess, "run")
    def test_runtime_status_normalizes_provider_data(self, run, _which):
        run.return_value = mock.Mock(
            returncode=0,
            stdout='{"Id":"2","Names":"web","Image":"demo","Status":"running"}\n',
            stderr="",
        )
        response = PLUGIN.server._dispatch(
            {"id": "2", "method": "v1.runtime.status", "params": {"engine": "podman"}, "trace_id": "trace-native", "operation_id": "op-native"}
        )
        self.assertEqual(response["result"]["containers"][0]["name"], "web")
        self.assertEqual(response["result"]["trace_id"], "trace-native")
        self.assertEqual(response["trace_id"], "trace-native")
        self.assertEqual(response["operation_id"], "op-native")
        run.assert_called_once_with(
            ["podman", "ps", "--all", "--format", "{{json .}}"],
            check=False,
            capture_output=True,
            text=True,
            timeout=10,
        )

    def test_logs_rejects_unbounded_tail(self):
        response = PLUGIN.server._dispatch(
            {"id": "3", "method": "v1.runtime.logs", "params": {"name": "web", "tail": 501}}
        )
        self.assertEqual(response["error"]["code"], 400)


if __name__ == "__main__":
    unittest.main()
