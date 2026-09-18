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

LIST_SIZE_HINT = {
    1: 178876, 3: 3547, 4: 0, 6: 13, 7: 140, 8: 0, 9: 0, 10: 958, 11: 3784,
    12: 12294, 13: 406, 14: 340, 15: 0, 16: 0, 17: 0, 19: 0, 20: 0, 21: 98935,
    22: 154, 24: 102241, 25: 375, 26: 0, 27: 247866, 29: 198579, 30: 38950,
    31: 0, 33: 0, 35: 94, 36: 34, 37: 1643, 39: 440, 40: 2071, 41: 107575,
    42: 46009, 43: 178, 46: 49584, 47: 422362, 49: 271702, 50: 2810, 52: 16271,
    53: 890, 54: 1533, 55: 1235, 56: 0, 57: 1370, 59: 0, 60: 345, 61: 200,
    62: 1727, 63: 387, 65: 233, 66: 483, 67: 108, 68: 9879, 69: 115677, 71: 0,
}


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
        parts = [blob(1, code.upper().encode())]
        parts.extend(blob(2, number(1, TYPE_DOMAIN) + blob(2, d.encode())) for d in domains)
        out += blob(1, b"".join(parts))
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
    try:
        meta = json.loads(fetch(f"{REGISTRY}/filters.json").decode())
        names = {f["filterId"]: f["name"] for f in meta.get("filters", [])}
    except Exception as exc:
        print(f"could not read the registry metadata: {exc}", file=sys.stderr)
        return 1
    if not names:
        print("the registry metadata carried no filter names", file=sys.stderr)
        return 1

    categories: dict[str, list[str]] = {}
    index = []
    wanted = sum(len(ids) for ids in GROUPS.values())
    failed: list[int] = []
    for group, ids in GROUPS.items():
        for filter_id in ids:
            try:
                raw = fetch(f"{REGISTRY}/filter_{filter_id}.txt")
            except Exception as exc:
                print(f"  could not fetch filter_{filter_id}: {exc}", file=sys.stderr)
                failed.append(filter_id)
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

    allowed = int(os.environ.get("FILTER_ASSET_MAX_FAILURES", "2"))
    if len(failed) > allowed:
        print(
            f"{len(failed)} of {wanted} lists failed to download ({failed}); "
            "refusing to ship a partial security asset. "
            "Raise FILTER_ASSET_MAX_FAILURES to accept it deliberately.",
            file=sys.stderr,
        )
        return 1

    for group, ids in GROUPS.items():
        lost = [i for i in ids if i in failed]
        if not lost:
            continue
        survivors = [e for e in index if e["group"] == group]
        if not survivors:
            print(
                f"group '{group}' lost every list it had ({lost}); "
                "a category that silently disappears is worse than a build that stops.",
                file=sys.stderr,
            )
            return 1
        kept = sum(e["domains"] for e in survivors)
        share = kept / max(1, kept + sum(LIST_SIZE_HINT.get(i, 0) for i in lost))
        if share < 0.5:
            print(
                f"group '{group}' kept only {share:.0%} of its coverage after losing {lost}; "
                "refusing rather than publishing a category that looks complete and is not.",
                file=sys.stderr,
            )
            return 1

    payload = encode(categories)
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "pgfilter.dat"), "wb") as handle:
        handle.write(payload)

    import hashlib

    with open(os.path.join(out_dir, "pgfilter_index.json"), "w", encoding="utf-8") as handle:
        json.dump(
            {
                "file": "pgfilter.dat",
                "sha256": hashlib.sha256(payload).hexdigest(),
                "lists": index,
                "failed": failed,
            },
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
