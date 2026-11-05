#!/usr/bin/env python3
"""Exercise shipped language CLIs offline; does not certify package-manager freeze."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

VERSION = "platform-factory.dev/language-inspection/v1"
FIXTURES = {
    "go": {"go.mod": "module example.com/fixture\n\ngo 1.26.10\n", "main.go": "package main\nfunc main() {}\n"},
    "python": {"main.py": "print('fixture')\n"},
    "node": {"package.json": '{"name":"fixture","version":"1.0.0"}', "index.js": "console.log('fixture')\n"},
    "ruby": {"Gemfile": "source 'https://example.invalid'\n", "app.rb": "puts 'fixture'\n"},
    "php": {"composer.json": '{"name":"fixture/app"}', "index.php": "<?php echo 'fixture';\n"},
    "java": {"pom.xml": "<project><modelVersion>4.0.0</modelVersion></project>"},
    "dotnet": {"Fixture.csproj": '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>'},
    "rust": {"Cargo.toml": '[package]\nname="fixture"\nversion="0.1.0"\nedition="2021"\n'},
}
DEPENDENCIES = {
    "ruby": "vendor/cache", "php": "vendor", "java": ".platform-factory/deps/java/m2",
    "dotnet": ".platform-factory/deps/dotnet", "rust": ".platform-factory/deps/rust",
}


def run(binary, *args, accepted=True):
    result = subprocess.run([str(binary), *map(str, args)], capture_output=True, timeout=20)
    if (result.returncode == 0) != accepted:
        raise RuntimeError(f"{binary.name} {args}: unexpected exit {result.returncode}: {result.stderr.decode(errors='replace')}")
    return result.stdout


def verify(bin_dir):
    results = []
    for language, fixture in FIXTURES.items():
        suffix = ".exe" if sys.platform == "win32" else ""
        binary = (bin_dir / f"platform-factory-lang-{language}{suffix}").resolve(strict=True)
        with tempfile.TemporaryDirectory(prefix="pf-language-contract-") as directory:
            root = Path(directory) / "project"
            root.mkdir()
            for name, contents in fixture.items():
                (root / name).write_text(contents)
            inspected = json.loads(run(binary, "inspect", "--root", root))
            if inspected.get("api_version") != VERSION or inspected.get("match") is not True or inspected.get("language") != language:
                raise RuntimeError(f"{language}: invalid inspection {inspected}")
            run(binary, "inspect", "--root", root / "missing", accepted=False)
            checks = ["versioned inspection", "missing root rejected"]
            if language in DEPENDENCIES:
                first, second = Path(directory) / "first.tar", Path(directory) / "second.tar"
                options = ["build-layer", "--root", root, "--dest", "app/deps"]
                run(binary, *options, "--output", first, accepted=False)
                if first.exists():
                    raise RuntimeError(f"{language}: failed build created output")
                deps = root / DEPENDENCIES[language]
                deps.mkdir(parents=True)
                (deps / "dependency.txt").write_bytes(b"offline fixture\n")
                run(binary, *options, "--output", first)
                run(binary, *options, "--output", second)
                data = first.read_bytes()
                if data != second.read_bytes():
                    raise RuntimeError(f"{language}: non-deterministic layer")
                with tarfile.open(fileobj=io.BytesIO(data)) as archive:
                    if archive.extractfile("app/deps/dependency.txt").read() != b"offline fixture\n":
                        raise RuntimeError(f"{language}: incorrect layer content")
                checks.extend(["unfrozen input rejected", "deterministic layer and payload"])
            results.append({"language": language, "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "checks": checks})
    return {"scope": "offline CLI contracts, not real package-manager freeze or guest boot", "results": results}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(verify(args.bin_dir), indent=2))
