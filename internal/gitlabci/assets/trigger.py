"""Explicit, HTTPS-only GitLab pipeline trigger for lifecycle hook callbacks."""
import argparse
import json
import os
from pathlib import Path
import re
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("GitLab redirect refused")


def trigger(filename, event, send=None):
    path = Path(filename)
    info = path.lstat()
    if not path.is_absolute() or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid() or info.st_size > 16384:
        raise ValueError("trusted owner-only absolute config required")
    with path.open() as stream:
        if not os.path.samestat(info, os.fstat(stream.fileno())):
            raise ValueError("config changed during open")
        cfg = json.load(stream)
    if set(cfg) != {"version", "url", "project", "ref", "events"} or cfg["version"] != 1:
        raise ValueError("invalid trigger config")
    url = urllib.parse.urlsplit(cfg["url"])
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        raise ValueError("HTTPS GitLab origin required")
    if not re.fullmatch(r"[1-9][0-9]{0,15}", str(cfg["project"])) or not isinstance(cfg["ref"], str) or not re.fullmatch(r"[A-Za-z0-9_./-]{1,200}", cfg["ref"]):
        raise ValueError("fixed project and ref required")
    if not isinstance(cfg["events"], list) or not cfg["events"] or any(e not in ("command.failed", "command.succeeded") for e in cfg["events"]):
        raise ValueError("unsupported event selection")
    if event.get("command") in ("ci", "hooks") or os.environ.get("CI") == "true":
        return False  # Do not recursively trigger pipelines from pipeline commands.
    if event.get("kind") not in cfg["events"]:
        return False
    identifier = event.get("id", "")
    if not isinstance(identifier, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", identifier):
        raise ValueError("invalid event id")
    token = os.environ.get("PF_GITLAB_TRIGGER_TOKEN", "")
    if not token or len(token) > 4096:
        raise ValueError("PF_GITLAB_TRIGGER_TOKEN required")
    body = urllib.parse.urlencode({"token": token, "ref": cfg["ref"], "variables[PF_EVENT_ID]": identifier}).encode()
    request = urllib.request.Request(cfg["url"].rstrip("/") + "/api/v4/projects/" + str(cfg["project"]) + "/trigger/pipeline", data=body, method="POST")
    if send is None:
        send = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open
    # No automatic retries: timeout may mean the remote pipeline already exists.
    with send(request, timeout=15) as response:
        data = response.read(65537)
        if response.status != 201 or len(data) > 65536:
            raise ValueError("GitLab trigger rejected")
        result = json.loads(data)
        if not isinstance(result.get("id"), int):
            raise ValueError("invalid GitLab response")
    return True


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    try:
        raw = sys.stdin.buffer.read(65537)
        if len(raw) > 65536:
            raise ValueError("event budget exceeded")
        trigger(args.config, json.loads(raw))
    except Exception:
        # Server errors can reflect request secrets; never echo exception bodies.
        parser.exit(1, "GitLab trigger failed; inspect configuration and remote pipeline state before retrying\n")
