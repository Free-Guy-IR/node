import os
import re
import shutil
import subprocess
import sys
import tempfile

FETCH_STEPS = (
    "git init -q .",
    "git remote add origin",
    "git fetch -q --depth 1 origin",
    "git checkout -q FETCH_HEAD",
)


def chain_steps(dockerfile):
    lines = open(dockerfile).read().split("\n")
    stage = next(i for i, line in enumerate(lines) if line.startswith("FROM ") and line.rstrip().endswith("AS xray-builder"))
    start = next(i for i, line in enumerate(lines) if i > stage and line.startswith("RUN git init -q . \\"))
    steps = []
    for line in lines[start:]:
        text = line.rstrip()
        continued = text.endswith("\\")
        if continued:
            text = text[:-1].rstrip()
        text = text.strip()
        if text.startswith("RUN "):
            text = text[len("RUN "):]
        if text.startswith("&& "):
            text = text[len("&& "):]
        steps.append(text)
        if not continued:
            break
    return [step for step in steps if not step.startswith(FETCH_STEPS)]


def split_patch(path):
    parts = re.split(r"(?m)^(?=diff --git )", open(path).read())
    header = "" if parts[0].startswith("diff --git ") else parts[0]
    return header, [part for part in parts if part.startswith("diff --git ")]


def section_file(section):
    return section.split("\n", 1)[0].split(" b/", 1)[1]


def test_only(path):
    return path.endswith("_test.go") or path.startswith("testing/")


class Replay:
    def __init__(self, dockerfile, src, commit):
        self.steps = chain_steps(dockerfile)
        self.src = src
        self.commit = commit
        self.env = dict(os.environ)
        if sys.platform == "darwin":
            self.env["PATH"] = "/usr/bin:/bin:" + self.env.get("PATH", "")
        self.env["XRAY_COMMIT"] = commit
        self.env["GOFLAGS"] = ""
        self.env["GOTOOLCHAIN"] = "local"

    def reset(self):
        subprocess.run(["git", "-C", self.src, "checkout", "-q", "-f", self.commit], check=True)
        subprocess.run(["git", "-C", self.src, "clean", "-qfdx"], check=True)

    def chain(self, patchdir):
        self.reset()
        for step in self.steps:
            step = step.replace("/patches/", patchdir.rstrip("/") + "/")
            result = subprocess.run(["/bin/sh", "-c", step], cwd=self.src, env=self.env, capture_output=True, text=True)
            if result.returncode != 0:
                return step
        return None

    def build(self):
        env = dict(self.env, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
        result = subprocess.run(["go", "build", "-trimpath", "-o", os.devnull, "./main"], cwd=self.src, env=env, capture_output=True, text=True)
        lines = (result.stdout + result.stderr).strip().split("\n")
        return result.returncode, lines[-1] if lines else ""


def without_file(patches, workdir, target, dropped):
    for name in os.listdir(patches):
        if name.startswith("xray-") and name.endswith(".patch"):
            source = os.path.join(patches, name)
            destination = os.path.join(workdir, name)
            if name == target:
                header, sections = split_patch(source)
                kept = [section for section in sections if section_file(section) != dropped]
                open(destination, "w").write(header + "".join(kept))
            else:
                shutil.copy(source, destination)


def main():
    dockerfile, patches, src, commit = sys.argv[1:5]
    only = set(sys.argv[5].split(",")) if len(sys.argv) > 5 and sys.argv[5] else None
    replay = Replay(dockerfile, src, commit)
    failed = replay.chain(patches)
    if failed is not None:
        print("BASELINE FAIL at:", failed)
        return 1
    code, last = replay.build()
    if code != 0:
        print("BASELINE BUILD FAIL:", last)
        return 1
    print("BASELINE ok: every patch applies, every guard passes and ./main builds for linux/amd64", flush=True)
    targets = []
    for name in sorted(os.listdir(patches)):
        if name.startswith("xray-") and name.endswith(".patch"):
            for section in split_patch(os.path.join(patches, name))[1]:
                path = section_file(section)
                if not test_only(path) and (only is None or name + ":" + path in only):
                    targets.append((name, path))
    uncaught = []
    for name, path in targets:
        workdir = tempfile.mkdtemp(prefix="xray-guard-replay-")
        try:
            without_file(patches, workdir, name, path)
            failed = replay.chain(workdir)
            if failed is not None:
                print("CAUGHT by the guard chain:", name, path, "::", failed, flush=True)
                continue
            code, last = replay.build()
            if code != 0:
                print("CAUGHT by the build:", name, path, "::", last[:200], flush=True)
                continue
            uncaught.append(name + ":" + path)
            print("UNCAUGHT:", name, path, flush=True)
        finally:
            shutil.rmtree(workdir, ignore_errors=True)
    print("removed", len(targets), "non-test file diffs one at a time;", len(uncaught), "still applied, passed every guard and built")
    return 1 if uncaught else 0


if __name__ == "__main__":
    sys.exit(main())
