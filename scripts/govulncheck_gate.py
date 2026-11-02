#!/usr/bin/env python3
import datetime
import json
import os
import re
import sys

MAX_WAIVER_DAYS = 90
KNOWN_MESSAGES = ("config", "progress", "SBOM", "osv", "finding")
WAIVER_RE = re.compile(r"^(\S+) (\S+) (\S+) (\S+) (\S+) (\S(?:.*\S)?)$")
OSV_RE = re.compile(r"^GO-\d{4}-\d{4,}$")
MODULE_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._~/-]*$")
VERSION_RE = re.compile(r"^v\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
SHELL_SEPARATORS = ("&&", "||", ";", "|", "\\", "&")


class GateError(Exception):
    pass


class Waiver:
    def __init__(self, line, osv, module, version, expires, patch, reason):
        self.line = line
        self.osv = osv
        self.module = module
        self.version = version
        self.expires = expires
        self.patch = patch
        self.reason = reason
        self.problems = []

    def key(self):
        return (self.osv, self.module, self.version)


def parse_date(text, what):
    if not DATE_RE.match(text):
        raise GateError(f"{what} {text!r} is not a YYYY-MM-DD date")
    try:
        return datetime.date.fromisoformat(text)
    except ValueError:
        raise GateError(f"{what} {text!r} is not a real calendar date")


def gate_today():
    override = os.environ.get("GOVULN_GATE_TODAY")
    if override is None:
        return datetime.datetime.now(datetime.timezone.utc).date()
    return parse_date(override, "GOVULN_GATE_TODAY")


def read_stream(path):
    try:
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
    except (OSError, UnicodeDecodeError) as exc:
        raise GateError(f"cannot read the govulncheck stream {path}: {exc}")
    decoder = json.JSONDecoder()
    messages = []
    pos = 0
    while True:
        while pos < len(text) and text[pos].isspace():
            pos += 1
        if pos == len(text):
            break
        try:
            value, pos = decoder.raw_decode(text, pos)
        except json.JSONDecodeError as exc:
            raise GateError(f"{path} is not a govulncheck JSON stream: {exc.msg} at offset {exc.pos}")
        if not isinstance(value, dict) or len(value) != 1:
            raise GateError(f"{path}: message {len(messages) + 1} is not a single-key govulncheck message object")
        kind = next(iter(value))
        if kind not in KNOWN_MESSAGES:
            raise GateError(f"{path}: message {len(messages) + 1} has the unknown type {kind!r}")
        messages.append((kind, value[kind]))
    return messages


def check_config(messages, path):
    configs = [body for kind, body in messages if kind == "config"]
    if len(configs) != 1:
        raise GateError(f"{path} has {len(configs)} config messages, want exactly 1; govulncheck did not finish a scan")
    if messages[0][0] != "config":
        raise GateError(f"{path} does not start with the config message")
    config = configs[0]
    if not isinstance(config, dict):
        raise GateError(f"{path}: the config message is not an object")
    if config.get("scanner_name") != "govulncheck":
        raise GateError(f"{path}: scanner_name is {config.get('scanner_name')!r}, want 'govulncheck'")
    if config.get("scan_level") != "symbol":
        raise GateError(f"{path}: scan_level is {config.get('scan_level')!r}, want 'symbol'; a module or package scan cannot tell a called vulnerability from an imported one")
    return config


def frame_name(frame):
    name = frame.get("package") or frame.get("module") or "?"
    if frame.get("receiver"):
        name += "." + frame["receiver"].lstrip("*")
    if frame.get("function"):
        name += "." + frame["function"]
    return name


def collect(messages):
    summaries = {}
    called = {}
    imported = {}
    for kind, body in messages:
        if kind == "osv" and isinstance(body, dict) and isinstance(body.get("id"), str):
            summaries[body["id"]] = body.get("summary") or ""
        if kind != "finding":
            continue
        if not isinstance(body, dict):
            raise GateError("a finding message is not an object")
        osv = body.get("osv")
        trace = body.get("trace")
        if not isinstance(osv, str) or not osv or not isinstance(trace, list) or not trace or not all(isinstance(frame, dict) for frame in trace):
            raise GateError(f"a finding for {osv!r} has no usable trace")
        frame = trace[0]
        key = (osv, frame.get("module") or "", frame.get("version") or "")
        if frame.get("function"):
            entry = called.setdefault(key, {"paths": 0, "fixed": body.get("fixed_version") or "", "example": trace})
            entry["paths"] += 1
        else:
            level = "package" if frame.get("package") else "module"
            imported.setdefault(key, set()).add(level)
    return called, imported, summaries


def read_waivers(path):
    try:
        with open(path, encoding="utf-8") as handle:
            lines = handle.read().splitlines()
    except (OSError, UnicodeDecodeError) as exc:
        raise GateError(f"cannot read the waiver file {path}: {exc}")
    waivers = []
    problems = []
    seen = {}
    for number, raw in enumerate(lines, 1):
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        match = WAIVER_RE.match(raw)
        if not match:
            problems.append(f"waiver line {number} is not '<OSV id> <module> <version> <expires YYYY-MM-DD> <patch file> <reason>' with single spaces")
            continue
        osv, module, version, expires_text, patch, reason = match.groups()
        bad = []
        if not OSV_RE.match(osv):
            bad.append(f"{osv!r} is not a GO-YYYY-NNNN id")
        if not MODULE_RE.match(module):
            bad.append(f"{module!r} is not a module path")
        if not VERSION_RE.match(version):
            bad.append(f"{version!r} is not an exact module version")
        try:
            expires = parse_date(expires_text, "the expiry")
        except GateError as exc:
            bad.append(str(exc))
            expires = None
        if bad:
            problems.append(f"waiver line {number}: " + "; ".join(bad))
            continue
        if osv in seen:
            problems.append(f"waiver line {number} repeats {osv}, already waived on line {seen[osv]}")
            continue
        seen[osv] = number
        waivers.append(Waiver(number, osv, module, version, expires, patch, reason))
    return waivers, problems


def applied_patch_names(dockerfile):
    names = set()
    for raw in dockerfile.splitlines():
        stripped = raw.strip()
        if stripped.startswith("#"):
            continue
        tokens = stripped.split()
        for index in range(len(tokens) - 1):
            if tokens[index] != "git" or tokens[index + 1] != "apply":
                continue
            for token in tokens[index + 2:]:
                if token in SHELL_SEPARATORS:
                    break
                ends_command = token.endswith(";")
                token = token.rstrip(";").strip("'\"")
                if token and not token.startswith("-"):
                    names.add(os.path.basename(token))
                if ends_command:
                    break
    return names


def check_waiver(waiver, today, root, applied, dockerfile_problem):
    if today > waiver.expires:
        waiver.problems.append(f"expired on {waiver.expires.isoformat()}; fix the vulnerability or renew the waiver with a fresh review")
    elif (waiver.expires - today).days > MAX_WAIVER_DAYS:
        waiver.problems.append(f"expires {waiver.expires.isoformat()}, more than {MAX_WAIVER_DAYS} days after {today.isoformat()}")
    patch = waiver.patch
    real_root = os.path.realpath(root)
    target = os.path.realpath(os.path.join(real_root, patch))
    if os.path.isabs(patch) or ".." in patch.split("/") or not target.startswith(real_root + os.sep):
        waiver.problems.append(f"{patch} is not a path inside the repository")
        return
    if not os.path.isfile(target):
        waiver.problems.append(f"{patch} does not exist")
    if dockerfile_problem:
        waiver.problems.append(dockerfile_problem)
    elif os.path.basename(patch) not in applied:
        waiver.problems.append(f"no 'git apply' line in the Dockerfile applies {os.path.basename(patch)}")


def run(waiver_path, stream_path, root):
    today = gate_today()
    messages = read_stream(stream_path)
    check_config(messages, stream_path)
    called, imported, summaries = collect(messages)
    waivers, problems = read_waivers(waiver_path)

    dockerfile_problem = ""
    applied = set()
    try:
        with open(os.path.join(root, "Dockerfile"), encoding="utf-8") as handle:
            applied = applied_patch_names(handle.read())
    except (OSError, UnicodeDecodeError) as exc:
        dockerfile_problem = f"cannot read the Dockerfile in {root}: {exc}"
    for waiver in waivers:
        check_waiver(waiver, today, root, applied, dockerfile_problem)

    by_key = {waiver.key(): waiver for waiver in waivers}
    used = set()
    waived = 0
    failing = 0
    for key in sorted(called):
        osv, module, version = key
        entry = called[key]
        waiver = by_key.get(key)
        where = f"{osv} {module}@{version}"
        if waiver is not None:
            used.add(key)
        if waiver is not None and not waiver.problems:
            waived += 1
            print(f"WAIVED {where} until {waiver.expires.isoformat()}: {waiver.reason} ({entry['paths']} call paths)")
            continue
        failing += 1
        example = entry["example"]
        detail = summaries.get(osv, "")
        fixed = entry["fixed"] or "no fixed version"
        status = f"its waiver on line {waiver.line} is not active" if waiver is not None else "no waiver"
        print(f"FAIL {where} is called ({entry['paths']} call paths), {status}; fixed in {fixed}; {detail}")
        print(f"     e.g. {frame_name(example[-1])} -> {frame_name(example[0])}")

    for key in sorted(imported):
        osv, module, version = key
        if key in called:
            continue
        levels = "/".join(sorted(imported[key]))
        print(f"INFO {osv} {module}@{version} is {levels}-level only, not called: does not fail the gate")

    for waiver in waivers:
        if waiver.key() not in used:
            waiver.problems.append("matches no called vulnerability in this scan; a stale waiver must be removed")
        for problem in waiver.problems:
            problems.append(f"waiver line {waiver.line} ({waiver.osv} {waiver.module}@{waiver.version}): {problem}")
    for problem in problems:
        print(f"FAIL {problem}")

    informational = sum(1 for key in imported if key not in called)
    counts = f"{len(called)} called vulnerabilities ({waived} waived, {failing} failing), {informational} not called, {len(waivers)} waivers, {len(problems)} waiver problems"
    if failing or problems:
        print(f"govulncheck-gate: FAIL: {counts}")
        return 1
    print(f"govulncheck-gate: pass: {counts}")
    return 0


def main(argv):
    if len(argv) not in (3, 4):
        print("usage: python3 scripts/govulncheck_gate.py <waiver file> <govulncheck -format json output> [repo root]", file=sys.stderr)
        return 2
    root = argv[3] if len(argv) == 4 else os.getcwd()
    try:
        return run(argv[1], argv[2], root)
    except GateError as exc:
        print(f"FAIL {exc}")
        print("govulncheck-gate: FAIL: the scan cannot be trusted, so nothing is waived")
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
