import json
import os
import re
import sys
import urllib.request

REGISTRY = "https://adguardteam.github.io/HostlistsRegistry/assets"
RULE = re.compile(r"^\|\|([a-z0-9.\-_]+)\^?$", re.I)

GROUPS = {
    "ads": [1, 27, 24, 49, 3, 4, 53, 59, 33, 69],
    "security": [42, 30, 12, 11, 10, 55, 50, 9, 41, 62, 31, 8],
    "bypass": [52, 68, 54, 56, 71],
    "devices": [60, 61, 63, 65, 66, 67, 7, 6, 39],
    "content": [47, 46, 57, 37],
    "regional": [13, 14, 15, 16, 17, 19, 20, 21, 22, 25, 26, 29, 35, 36, 40, 43],
}

TYPE_DOMAIN = 2


def varint(value: int) -> bytes:
    out = bytearray()
    while True:
        chunk = value & 0x7F
        value >>= 7
        if value:
            out.append(chunk | 0x80)
        else:
            out.append(chunk)
            return bytes(out)


def tag(field: int, wire: int) -> bytes:
    return varint((field << 3) | wire)


def blob(field: int, payload: bytes) -> bytes:
    return tag(field, 2) + varint(len(payload)) + payload


def number(field: int, value: int) -> bytes:
    return tag(field, 0) + varint(value)


def encode(categories: dict[str, list[str]]) -> bytes:
    out = bytearray()
    for code, domains in categories.items():
        body = blob(1, code.upper().encode())
        for domain in domains:
            body += blob(2, number(1, TYPE_DOMAIN) + blob(2, domain.encode()))
        out += blob(1, body)
    return bytes(out)


def fetch(url: str, timeout: int = 180) -> bytes:
    request = urllib.request.Request(url, headers={"User-Agent": "pg-node-asset-builder"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.read()


def domains_of(raw: bytes) -> list[str]:
    found = set()
    for line in raw.decode("utf-8", "replace").splitlines():
        line = line.strip()
        if not line or line[0] in "!#[":
            continue
        match = RULE.match(line)
        if match:
            host = match.group(1).strip(".").lower()
            if host and "." in host:
                found.add(host)
    return sorted(found)


def main() -> int:
    out_dir = sys.argv[1] if len(sys.argv) > 1 else "/usr/local/share/xray"
    names = {}
    try:
        meta = json.loads(fetch(f"{REGISTRY}/../filters.json").decode())
        names = {f["filterId"]: f["name"] for f in meta.get("filters", [])}
    except Exception:
        pass

    categories: dict[str, list[str]] = {}
    index = []
    for group, ids in GROUPS.items():
        for filter_id in ids:
            try:
                raw = fetch(f"{REGISTRY}/filter_{filter_id}.txt")
            except Exception as exc:
                print(f"  skipped filter_{filter_id}: {exc}", file=sys.stderr)
                continue
            found = domains_of(raw)
            if not found:
                continue
            key = f"pglist-{filter_id}"
            categories[key] = found
            index.append(
                {
                    "key": key,
                    "group": group,
                    "label": names.get(filter_id, str(filter_id)),
                    "domains": len(found),
                }
            )
            print(f"  {key:<14} {group:<9} {len(found):>9,} domains", file=sys.stderr)

    if not categories:
        print("no lists could be fetched", file=sys.stderr)
        return 1

    payload = encode(categories)
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "pgfilter.dat"), "wb") as handle:
        handle.write(payload)

    import hashlib

    with open(os.path.join(out_dir, "pgfilter_index.json"), "w", encoding="utf-8") as handle:
        json.dump(
            {"file": "pgfilter.dat", "sha256": hashlib.sha256(payload).hexdigest(), "lists": index},
            handle,
            ensure_ascii=False,
            indent=1,
        )

    total = sum(len(v) for v in categories.values())
    print(
        f"  pgfilter.dat: {len(categories)} lists, {total:,} domains, {len(payload) / 1048576:.1f} MB",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
