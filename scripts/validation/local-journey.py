#!/usr/bin/env python3
"""Offline acceptance of a prepared FastAPI fixture; never installs or fetches tools.

The fixture must already contain app.py, requirements.{txt,lock}, pf.{yaml,lock}
and .platform-factory/deps/python/runtime. Wheel files and the base image must
already be local. Results describe these fixtures, not onboarding or adoption.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import statistics
import subprocess
import sys
import time
import urllib.request
import uuid


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def free_port():
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pf", type=Path, required=True)
    parser.add_argument("--python", type=Path, required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--wheelhouse", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--runs", type=int, default=10)
    parser.add_argument("--baseline-runs", type=int, default=3)
    parser.add_argument("--engine", default="podman", choices=["podman", "docker"])
    parser.add_argument("--base-image", required=True, help="Already cached digest-pinned Python base")
    args = parser.parse_args()
    if not __debug__:
        parser.error("run without Python -O so acceptance assertions remain enabled")
    if min(args.runs, args.baseline_runs) < 1 or "@sha256:" not in args.base_image:
        parser.error("positive run counts and a digest-pinned base image are required")
    for name in ["pf", "fixture", "wheelhouse"]:
        setattr(args, name, getattr(args, name).resolve(strict=True))
    args.python = args.python.absolute()
    if not args.python.is_file():
        parser.error("--python must name an existing executable")
    args.results.mkdir(parents=True, exist_ok=False)
    args.results = args.results.resolve()
    token = uuid.uuid4().hex[:10]
    prefix = "pf-local-" + token
    owned_containers, owned_images, owned_workspaces = set(), set(), []
    report = {"scope": "Prepared Linux/amd64 FastAPI fixture, existing tools and caches; no adoption or cold-install claim",
              "host": platform.platform(), "pf_sha256": digest(args.pf), "fixture_sha256": {},
              "offline": "pip --no-index; local wheelhouse; container builds --pull=never --network=none",
              "trials": [], "faults": [], "baseline": [], "cleanup_errors": []}
    env = {**os.environ, "PATH": str(args.python.parent) + os.pathsep + os.environ["PATH"],
           "PIP_NO_INDEX": "1", "PIP_FIND_LINKS": str(args.wheelhouse), "PIP_CONFIG_FILE": os.devnull,
           "PIP_PLATFORM": "any", "PIP_PYTHON_VERSION": "3.12", "PIP_ONLY_BINARY": ":all:",
           "PIP_DISABLE_PIP_VERSION_CHECK": "1", "GOPROXY": "off", "GOSUMDB": "off"}
    environment_overrides = {k: v for k, v in env.items() if k.startswith("PIP_")}
    report["dependency_environment"] = environment_overrides
    for name in ["app.py", "requirements.txt", "requirements.lock", "pf.yaml", "pf.lock"]:
        report["fixture_sha256"][name] = digest(args.fixture / name)

    def save():
        (args.results / "report.json").write_text(json.dumps(report, indent=2))

    def run(argv, cwd=None, allowed=(0,), custom_env=None, timeout=120):
        started = time.monotonic()
        result = subprocess.run([str(x) for x in argv], cwd=cwd, env=custom_env or env,
                                capture_output=True, text=True, timeout=timeout)
        entry = {"argv": [str(x) for x in argv], "cwd": str(cwd) if cwd else None,
                 "exit_code": result.returncode, "seconds": round(time.monotonic() - started, 4),
                 "stdout": result.stdout, "stderr": result.stderr}
        with (args.results / "commands.jsonl").open("a") as log:
            log.write(json.dumps(entry) + "\n")
        if allowed is not None and result.returncode not in allowed:
            raise RuntimeError(f"{argv[0]} exited {result.returncode}: {result.stderr[-1500:]}")
        return entry

    def pf(work, *argv, **kwargs):
        return run([args.pf, *argv], cwd=work, **kwargs)

    def seed(label):
        work = args.results / (prefix + "-" + label)
        work.mkdir()
        owned_workspaces.append(work)
        for name in report["fixture_sha256"]:
            shutil.copy2(args.fixture / name, work / name)
        shutil.copytree(args.fixture / ".platform-factory/deps/python/runtime",
                        work / ".platform-factory/deps/python/runtime")
        return work

    def build(work):
        result = pf(work, "build", "--yes")
        reference = json.loads(result["stdout"])["reference"]
        if token not in reference:
            raise RuntimeError("fixture must not pin a shared image name; use a unique workspace-derived reference")
        owned_images.add(reference)
        return result

    def stop(name):
        if name not in owned_containers:
            return
        if not name.startswith(prefix):
            raise RuntimeError("refusing cleanup of an unowned container")
        run([args.engine, "rm", "--force", name], allowed=None)

    def ready(port):
        started = time.monotonic()
        for _ in range(100):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=1) as response:
                    body = json.load(response)
                    if response.status != 200 or body != {"message": "Hello World"}:
                        raise RuntimeError("unexpected HTTP response")
                    return round(time.monotonic() - started, 4)
            except (OSError, ValueError):
                time.sleep(0.1)
        raise RuntimeError("FastAPI did not become ready")

    def launch(work, name, port, allowed=(0,)):
        owned_containers.add(name)
        return pf(work, "run", "--detach", "--runtime", args.engine, "--name", name,
                  "-p", f"127.0.0.1:{port}:8000", allowed=allowed)

    def inspect(name):
        value = json.loads(run([args.engine, "inspect", name])["stdout"])[0]
        if not value["State"]["Running"]:
            raise RuntimeError("container is not running")
        return value

    def fault(name, operation):
        record = {"name": name, "passed": False}
        try:
            operation()
            record["passed"] = True
        except Exception as error:
            record["error"] = str(error)
        report["faults"].append(record)
        save()
        print("Fault", name, "PASS" if record["passed"] else "FAIL", flush=True)

    try:
        report["engine_version"] = run([args.engine, "version"])["stdout"]
        run([args.engine, "image", "inspect", args.base_image])  # Lookup only; never pull.
        initial_ids = set(run([args.engine, "ps", "-aq"])["stdout"].split())
        report["preexisting_container_ids"] = sorted(initial_ids)
        baseline_inputs = args.results / "baseline-inputs"
        for number in range(1, args.runs + 1):
            work = seed(f"trial-{number}")
            name = f"{prefix}-trial-{number}"
            trial = {"number": number, "passed": False, "cache": "prepared runtime and wheelhouse; new dependency target and OCI output"}
            started = time.monotonic()
            try:
                trial["freeze_seconds"] = pf(work, "project", "freeze")["seconds"]
                trial["build_seconds"] = build(work)["seconds"]
                pf(work, "verify", "--format", "text", ".platform-factory/image")
                port = free_port()
                trial["run_seconds"] = launch(work, name, port)["seconds"]
                trial["http_ready_seconds"] = ready(port)
                value = inspect(name)
                trial["image_bytes"] = json.loads(run([args.engine, "image", "inspect", value["Image"]])["stdout"])[0]["Size"]
                trial["manifest_digest"] = json.loads((work / ".platform-factory/image/index.json").read_text())["manifests"][0]["digest"]
                trial["root_entries"] = sorted(p.name for p in work.iterdir())
                trial["passed"] = True
                if number == 1:
                    baseline_inputs.mkdir()
                    shutil.copy2(work / "app.py", baseline_inputs / "app.py")
                    deps = work / ".platform-factory/deps/python"
                    shutil.copytree(deps, baseline_inputs / "deps", ignore=shutil.ignore_patterns("runtime"))
                stop(name)
                # Same image, second fresh container: explicit idempotent retry.
                retry = name + "-retry"
                retry_port = free_port()
                launch(work, retry, retry_port)
                ready(retry_port)
                stop(retry)
                trial["restart_passed"] = True
            except Exception as error:
                trial.update(passed=False, error=str(error))
            finally:
                stop(name)
                trial["total_seconds"] = round(time.monotonic() - started, 4)
                report["trials"].append(trial)
                save()
                print("Trial", number, "PASS" if trial["passed"] else "FAIL", flush=True)

        failed = seed("missing-cache")
        original_lock = (failed / "requirements.lock").read_bytes()
        empty = args.results / "empty-wheels"
        empty.mkdir()

        def missing_cache():
            result = pf(failed, "project", "freeze", allowed=None,
                        custom_env={**env, "PIP_FIND_LINKS": str(empty)})
            assert result["exit_code"] != 0, "missing cache reported success"
            assert (failed / "requirements.lock").read_bytes() == original_lock
            assert not (failed / ".platform-factory/freeze.lock.json").exists()
            pf(failed, "project", "freeze")
            build(failed)
        fault("missing-cache-and-recovery", missing_cache)

        def unavailable_dependency():
            work = seed("unavailable-dependency")
            marker = b"pf-nonexistent-local-fixture==0.0.0\n"
            (work / "requirements.lock").write_bytes(marker)
            result = pf(work, "project", "freeze", allowed=None)
            assert result["exit_code"] != 0
            assert (work / "requirements.lock").read_bytes() == marker
            assert not (work / ".platform-factory/freeze.lock.json").exists()
        fault("unavailable-dependency", unavailable_dependency)

        def stale_lock():
            work = seed("stale-lock")
            (work / "pf.yaml").write_text((work / "pf.yaml").read_text() + "\nimage: incompatible-plan\n")
            before = digest(work / "pf.lock")
            result = pf(work, "build", "--yes", allowed=None)
            assert result["exit_code"] != 0
            assert digest(work / "pf.lock") == before
            assert not (work / ".platform-factory/image").exists()
        fault("stale-plan-lock", stale_lock)

        def changed_source():
            work = seed("changed-source")
            pf(work, "project", "freeze")
            (work / "app.py").write_text((work / "app.py").read_text() + "\n# changed after freeze\n")
            result = pf(work, "build", "--yes", allowed=None)
            assert result["exit_code"] != 0
            assert not (work / ".platform-factory/image").exists()
            pf(work, "project", "freeze")
            build(work)
        fault("changed-source-and-recovery", changed_source)

        def failed_rebuild():
            work = seed("failed-rebuild")
            pf(work, "project", "freeze")
            build(work)
            image = work / ".platform-factory/image"
            previous = digest(image / "index.json")
            (work / "app.py").write_text((work / "app.py").read_text() + "\n# rebuild input\n")
            release = work / ".platform-factory/release"
            shutil.rmtree(release)
            release.write_text("force failure after OCI packaging")
            name, port = prefix + "-failed-rebuild", free_port()
            result = launch(work, name, port, allowed=None)
            assert result["exit_code"] != 0
            assert digest(image / "index.json") == previous
            pf(work, "verify", image)
            assert not list(image.parent.glob(".pf-rebuild-*"))
            release.unlink()
            launch(work, name, port)
            ready(port)
            inspect(name)
            assert digest(image / "index.json") != previous
            pf(work, "verify", image)
            stop(name)
        fault("failed-rebuild-preserves-image-and-recovers", failed_rebuild)

        def tampered_image():
            copy = args.results / "tampered-image"
            shutil.copytree(failed / ".platform-factory/image", copy)
            pf(failed, "verify", copy)
            descriptor = json.loads((copy / "index.json").read_text())["manifests"][0]
            blob = copy / "blobs/sha256" / descriptor["digest"].split(":")[1]
            blob.write_bytes(blob.read_bytes() + b"\n")
            assert pf(failed, "verify", copy, allowed=None)["exit_code"] != 0
        fault("tampered-manifest", tampered_image)

        def occupied_port():
            with socket.socket() as listener:
                listener.bind(("127.0.0.1", 0))
                listener.listen()
                name = prefix + "-occupied"
                result = launch(failed, name, listener.getsockname()[1], allowed=None)
                assert result["exit_code"] != 0, "occupied port reported success"
                stop(name)
            name = prefix + "-recovered-port"
            port = free_port()
            launch(failed, name, port)
            ready(port)
            stop(name)
        fault("occupied-port-and-recovery", occupied_port)

        def interrupted_freeze():
            work = seed("interrupted")
            shim = args.results / "interrupt-bin"
            shim.mkdir()
            pid_file = args.results / "producer.pid"
            script = f'''#!{args.python}
import os,sys,time
from pathlib import Path
if sys.argv[1:4] == ['-m','pip','freeze']:
 print('partial-lock-output',flush=True)
 Path({str(pid_file)!r}).write_text(str(os.getpid()))
 time.sleep(60)
else:
 os.execv({str(args.python)!r},[{str(args.python)!r},*sys.argv[1:]])
'''
            helper = shim / "python"
            helper.write_text(script)
            helper.chmod(0o755)
            old = (work / "requirements.lock").read_bytes()
            with (args.results / "interruption.log").open("w") as log:
                process = subprocess.Popen([str(args.pf), "project", "freeze"], cwd=work,
                                           env={**env, "PATH": str(shim) + os.pathsep + env["PATH"]},
                                           stdout=log, stderr=log, start_new_session=True)
                try:
                    deadline = time.monotonic() + 30
                    while not pid_file.exists() and process.poll() is None and time.monotonic() < deadline:
                        time.sleep(0.05)
                    assert pid_file.exists(), "producer did not reach interrupt point"
                    process.send_signal(signal.SIGINT)
                    assert process.wait(timeout=10) != 0, "interruption reported success"
                    child = int(pid_file.read_text())
                    try:
                        os.kill(child, 0)
                    except ProcessLookupError:
                        pass
                    else:
                        raise AssertionError("producer survived interruption")
                    assert (work / "requirements.lock").read_bytes() == old
                    assert not list(work.glob(".pf-freeze-*"))
                    assert not (work / ".platform-factory/freeze.lock.json").exists()
                finally:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait()
            pf(work, "project", "freeze")
            build(work)
        fault("interruption-preserves-lock-and-recovers", interrupted_freeze)

        dockerfile = baseline_inputs / "Dockerfile"
        dockerfile.write_text(f'''FROM {args.base_image}
COPY app.py /app/app.py
COPY deps /deps
ENV PYTHONPATH=/deps
USER 65532:65532
WORKDIR /app
ENTRYPOINT ["python3", "/app/app.py"]
''')
        report["comparison_limits"] = [
            "Both paths use the same app, pinned Python image and pure-Python frozen dependencies; HTTP and runtime restrictions are checked.",
            "PF packages selected runtime files and verifies its OCI layout; baseline retains the base OS and relies on engine image inspection.",
            "Prepared caches only; installation, novice effort, long-term maintenance and retention are unmeasured.",
            "PF builds on the host while the engine builds inside its local VM on this host; timings are specific to this topology."]
        for number in range(1, args.baseline_runs + 1):
            image = f"localhost/{prefix}-baseline:{number}"
            name = f"{prefix}-baseline-{number}"
            owned_images.add(image)
            owned_containers.add(name)
            item = {"number": number, "passed": False}
            try:
                item["build_seconds"] = run([args.engine, "build", "--pull=never" if args.engine == "podman" else "--pull=false", "--network=none", "--no-cache",
                                              "--platform=linux/amd64", "-t", image, "-f", dockerfile, baseline_inputs])["seconds"]
                port = free_port()
                item["run_seconds"] = run([args.engine, "run", "--detach", "--rm", "--init", "--name", name,
                                            "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                                            "--network=bridge", "--cpus=1", "--memory=128m", "--pids-limit=128",
                                            "--tmpfs=/tmp:rw,noexec,nosuid,size=16m", "-p", f"127.0.0.1:{port}:8000", image])["seconds"]
                item["http_ready_seconds"] = ready(port)
                inspect(name)
                item["image_bytes"] = json.loads(run([args.engine, "image", "inspect", image])["stdout"])[0]["Size"]
                item["passed"] = True
            except Exception as error:
                item["error"] = str(error)
            finally:
                stop(name)
                report["baseline"].append(item)
                save()
                print("Baseline", number, "PASS" if item["passed"] else "FAIL", flush=True)
    finally:
        for name in owned_containers:
            try:
                stop(name)
            except Exception as error:
                report["cleanup_errors"].append(str(error))
        for image in owned_images:
            result = run([args.engine, "image", "rm", image], allowed=None)
            missing = any(message in result["stderr"].lower() for message in
                          ["no such image", "image not known", "no such object"])
            if result["exit_code"] and not missing:
                report["cleanup_errors"].append(result["stderr"])
        for work in owned_workspaces:
            shutil.rmtree(work)
        report["owned_project_directories_removed"] = all(not p.exists() for p in owned_workspaces)
        save()
    remaining = set(run([args.engine, "ps", "-aq"])["stdout"].split())
    report["preexisting_containers_preserved"] = initial_ids <= remaining
    report["owned_containers_remaining"] = run([args.engine, "ps", "-a", "--filter", "name=" + prefix, "--format", "{{.Names}}"])["stdout"].split()
    report["summary"] = {"nominal_passed": sum(t["passed"] for t in report["trials"]), "nominal_attempted": len(report["trials"]),
                         "faults_passed": sum(t["passed"] for t in report["faults"]), "faults_attempted": len(report["faults"]),
                         "baseline_passed": sum(t["passed"] for t in report["baseline"]), "baseline_attempted": len(report["baseline"])}
    for group in ["trials", "baseline"]:
        good = [item for item in report[group] if item["passed"]]
        if good:
            report[group + "_medians"] = {key: statistics.median(item[key] for item in good) for key in ["build_seconds", "image_bytes"]}
    save()
    print(json.dumps(report["summary"], indent=2))
    return 0 if (report["summary"]["nominal_passed"] == args.runs and all(t["passed"] for t in report["faults"])
                 and report["summary"]["baseline_passed"] == args.baseline_runs and not report["cleanup_errors"]
                 and report["preexisting_containers_preserved"] and not report["owned_containers_remaining"]) else 1


if __name__ == "__main__":
    sys.exit(main())
