import argparse
import asyncio
import faulthandler
import json
import os
import socket
import statistics
import struct
import subprocess
import sys
import threading
import time
import uuid

LOOP = "127.0.0.1"
PRIVATE = "10.77.0.1"
PUBLIC = "1.2.3.4"
CGNAT = "100.64.0.1"
TUNNEL_DEV = "pgtun0"
TUNNEL_NET = "10.88.0.1/24"
TUNNEL_TARGET = "10.88.0.5"
ZONE = "pgtest"

TYPE_A = 1
TYPE_AAAA = 28
RCODE_OK = 0
RCODE_NXDOMAIN = 3

PRIVATE_CIDRS = [
    "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
    "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
    "203.0.113.0/24", "224.0.0.0/3", "::/127", "fc00::/7", "fe80::/10", "ff00::/8",
]

NODE_PORTS = {"india": 20001, "egypt": 20002, "plain": 20003}
CLIENT_PORTS = {"india": 10801, "egypt": 10802, "plain": 10803}
CURRENT = ["starting"]


def say(*parts):
    print(time.strftime("%H:%M:%S"), " ".join(str(p) for p in parts), flush=True)


def step(text):
    CURRENT[0] = text
    say("==", text)


def print_core_logs(out):
    for name in sorted(os.listdir(out)):
        if name.endswith(".log"):
            with open(os.path.join(out, name)) as handle:
                say("==== last lines of", name)
                print("".join(handle.readlines()[-40:]), flush=True)


def watchdog(budget, out):
    started = time.time()
    while True:
        time.sleep(60)
        elapsed = time.time() - started
        say("watchdog: %.0f s in, at %s" % (elapsed, CURRENT[0]))
        if elapsed > budget:
            say("watchdog: over the %d s budget, every thread's stack and the core logs follow" % budget)
            faulthandler.dump_traceback(all_threads=True)
            print_core_logs(out)
            os._exit(3)


def run(cmd, check=True):
    say("$", " ".join(cmd))
    proc = subprocess.run(cmd, capture_output=True, text=True)
    for stream in (proc.stdout, proc.stderr):
        if stream.strip():
            say(stream.strip())
    if check and proc.returncode != 0:
        raise SystemExit("command failed: " + " ".join(cmd))
    return proc


def read_question(packet):
    qid, flags = struct.unpack("!HH", packet[:4])
    pos, labels = 12, []
    while packet[pos]:
        size = packet[pos]
        labels.append(packet[pos + 1:pos + 1 + size].decode("ascii", "replace"))
        pos += 1 + size
    qtype, _ = struct.unpack("!HH", packet[pos + 1:pos + 5])
    return qid, flags, ".".join(labels).lower(), qtype, packet[12:pos + 5]


def build_reply(qid, flags, question, rcode, addresses):
    records = b""
    for address in addresses:
        if ":" in address:
            records += struct.pack("!HHHIH", 0xC00C, TYPE_AAAA, 1, 0, 16) + socket.inet_pton(socket.AF_INET6, address)
        else:
            records += struct.pack("!HHHIH", 0xC00C, TYPE_A, 1, 0, 4) + socket.inet_aton(address)
    header = struct.pack("!HHHHHH", qid, 0x8400 | (flags & 0x0100) | 0x0080 | rcode, 1, len(addresses), 0, 0)
    return header + question + records


class ScriptedDNS:
    def __init__(self, upstream):
        self.upstream = upstream
        self.delay = 0.0
        self.lock = threading.Lock()
        self.asked = {}
        self.log = []
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind((LOOP, 53))
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                packet, peer = self.sock.recvfrom(4096)
            except OSError:
                return
            threading.Thread(target=self.handle, args=(packet, peer), daemon=True).start()

    def handle(self, packet, peer):
        try:
            qid, flags, name, qtype, question = read_question(packet)
        except (IndexError, struct.error):
            return
        if not name.endswith("." + ZONE):
            self.forward(packet, peer)
            return
        if self.delay:
            time.sleep(self.delay)
        rcode, addresses = self.answer(name, qtype)
        try:
            self.sock.sendto(build_reply(qid, flags, question, rcode, addresses), peer)
        except OSError:
            pass

    def forward(self, packet, peer):
        if not self.upstream:
            return
        up = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        up.settimeout(3)
        try:
            up.sendto(packet, (self.upstream, 53))
            self.sock.sendto(up.recvfrom(65535)[0], peer)
        except OSError:
            pass
        finally:
            up.close()

    def answer(self, name, qtype):
        with self.lock:
            key = (name, qtype)
            self.asked[key] = self.asked.get(key, 0) + 1
            lookup = self.asked[key]
            rcode, addresses = self.decide(name.split(".", 1)[0], qtype, lookup)
            self.log.append((name, qtype, lookup, rcode, tuple(addresses)))
            return rcode, addresses

    @staticmethod
    def decide(label, qtype, lookup):
        if qtype not in (TYPE_A, TYPE_AAAA):
            return RCODE_OK, []
        if label.startswith("failfirst-"):
            if lookup == 1:
                return RCODE_NXDOMAIN, []
            return RCODE_OK, [PRIVATE] if qtype == TYPE_A else []
        if qtype == TYPE_AAAA:
            return RCODE_OK, []
        if label.startswith("rebind-tunnel-"):
            return RCODE_OK, [PUBLIC if lookup % 2 == 1 else TUNNEL_TARGET]
        if label.startswith("rebind-"):
            return RCODE_OK, [PUBLIC if lookup % 2 == 1 else PRIVATE]
        if label.startswith("private-"):
            return RCODE_OK, [PRIVATE]
        if label.startswith("tunnel-"):
            return RCODE_OK, [TUNNEL_TARGET]
        if label.startswith("public-") or label.startswith("perf-"):
            return RCODE_OK, [PUBLIC]
        return RCODE_NXDOMAIN, []

    def queries(self, prefix):
        with self.lock:
            return [entry for entry in self.log if entry[0].startswith(prefix)]


class Sink:
    def __init__(self, family, address, port):
        self.name = "[%s]:%d" % (address, port) if family == socket.AF_INET6 else "%s:%d" % (address, port)
        self.lock = threading.Lock()
        self.accepts = 0
        self.received = 0
        self.sock = socket.socket(family, socket.SOCK_STREAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind(socket.getaddrinfo(address, port, family, socket.SOCK_STREAM)[0][4])
        self.sock.listen(4096)
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                conn, _ = self.sock.accept()
            except OSError:
                return
            with self.lock:
                self.accepts += 1
            threading.Thread(target=self.reply, args=(conn,), daemon=True).start()

    def reply(self, conn):
        conn.settimeout(10)
        data = b""
        try:
            while b"\r\n\r\n" not in data:
                chunk = conn.recv(4096)
                if not chunk:
                    break
                data += chunk
            with self.lock:
                self.received += len(data)
            if data:
                conn.sendall(b"HTTP/1.0 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
        except OSError:
            pass
        finally:
            conn.close()

    def snapshot(self):
        with self.lock:
            return self.accepts, self.received


def recv_exact(sock, size):
    data = b""
    while len(data) < size:
        chunk = sock.recv(size - len(data))
        if not chunk:
            raise OSError("closed after %d of %d bytes" % (len(data), size))
        data += chunk
    return data


def socks_address(host, port):
    try:
        return b"\x01" + socket.inet_aton(host) + struct.pack("!H", port)
    except OSError:
        encoded = host.encode()
        return b"\x03" + bytes([len(encoded)]) + encoded + struct.pack("!H", port)


def socks_get(socks_port, host, port, timeout, host_header=None):
    start = time.perf_counter()
    body, error = b"", ""
    try:
        sock = socket.create_connection((LOOP, socks_port), timeout=timeout)
    except OSError as exc:
        return time.perf_counter() - start, b"", "socks connect: %s" % exc
    try:
        sock.settimeout(timeout)
        sock.sendall(b"\x05\x01\x00")
        recv_exact(sock, 2)
        sock.sendall(b"\x05\x01\x00" + socks_address(host, port))
        head = recv_exact(sock, 4)
        recv_exact(sock, {1: 6, 4: 18}.get(head[3]) or recv_exact(sock, 1)[0] + 2)
        sock.sendall(("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n" % (host_header or host)).encode())
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            body += chunk
    except OSError as exc:
        error = str(exc) or exc.__class__.__name__
    finally:
        sock.close()
    return time.perf_counter() - start, body, error


class TunnelCounter:
    def __init__(self, port):
        self.port = port
        run(["iptables", "-w", "-I", "OUTPUT", "1", "-o", TUNNEL_DEV, "-d", TUNNEL_TARGET + "/32", "-p", "tcp", "--dport", str(port)])

    def packets(self):
        listing = subprocess.run(["iptables", "-w", "-L", "OUTPUT", "-v", "-x", "-n"], capture_output=True, text=True, check=True).stdout
        for line in listing.splitlines():
            fields = line.split()
            if fields and TUNNEL_TARGET in fields and "dpt:%d" % self.port in fields:
                return int(fields[0])
        raise SystemExit("the iptables counter for %s:%d is gone" % (TUNNEL_TARGET, self.port))


class UDPSink:
    def __init__(self, address, port):
        self.name = "%s:%d" % (address, port)
        self.lock = threading.Lock()
        self.datagrams = 0
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind((address, port))
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                self.sock.recvfrom(4096)
            except OSError:
                return
            with self.lock:
                self.datagrams += 1

    def count(self):
        with self.lock:
            return self.datagrams


def socks_stream(socks_port, host, port, payload, hold):
    try:
        sock = socket.create_connection((LOOP, socks_port), timeout=hold + 5)
    except OSError as exc:
        return "socks connect: %s" % exc
    try:
        sock.sendall(b"\x05\x01\x00")
        recv_exact(sock, 2)
        sock.sendall(b"\x05\x01\x00" + socks_address(host, port))
        head = recv_exact(sock, 4)
        recv_exact(sock, {1: 6, 4: 18}.get(head[3]) or recv_exact(sock, 1)[0] + 2)
        sock.sendall(payload)
        time.sleep(hold)
        return ""
    except OSError as exc:
        return str(exc) or exc.__class__.__name__
    finally:
        sock.close()


def link_local_address():
    proc = subprocess.run(["ip", "-6", "-o", "addr", "show", "scope", "link"], capture_output=True, text=True)
    for line in proc.stdout.splitlines():
        parts = line.split()
        if len(parts) >= 4 and parts[1] not in ("lo", TUNNEL_DEV) and parts[3].startswith("fe80"):
            return parts[3].split("/")[0] + "%" + parts[1]
    return None


def wait_port(port, within=20.0):
    deadline = time.time() + within
    while time.time() < deadline:
        try:
            socket.create_connection((LOOP, port), timeout=0.5).close()
            return True
        except OSError:
            time.sleep(0.1)
    return False


def node_config(user):
    sniffing = {"enabled": True, "routeOnly": False, "destOverride": ["http", "tls"]}

    def inbound(tag):
        return {"tag": tag, "listen": LOOP, "port": NODE_PORTS[tag], "protocol": "vless", "sniffing": sniffing,
                "settings": {"clients": [{"id": user, "email": "harness@example.test"}], "decryption": "none"}}

    return {
        "log": {"loglevel": "warning"},
        "dns": {"servers": ["localhost"]},
        "inbounds": [inbound(tag) for tag in NODE_PORTS],
        "outbounds": [
            {"tag": "DIRECT", "protocol": "freedom", "settings": {"domainStrategy": "UseIPv4", "privateAllow": [{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]}},
            {"tag": "india_wireguard", "protocol": "freedom", "settings": {}, "streamSettings": {"sockopt": {"interface": TUNNEL_DEV}}},
            {"tag": "egypt_wireguard", "protocol": "freedom", "settings": {}, "streamSettings": {"sockopt": {"interface": "lo"}}},
            {"tag": "asis", "protocol": "freedom", "settings": {}},
            {"tag": "BLOCK", "protocol": "blackhole", "settings": {}},
        ],
        "routing": {"domainStrategy": "IPIfNonMatch", "rules": [
            {"type": "field", "port": "25", "outboundTag": "BLOCK"},
            {"type": "field", "ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"], "outboundTag": "DIRECT"},
            {"type": "field", "ip": PRIVATE_CIDRS, "outboundTag": "BLOCK"},
            {"type": "field", "inboundTag": ["india"], "outboundTag": "india_wireguard"},
            {"type": "field", "inboundTag": ["egypt"], "outboundTag": "egypt_wireguard"},
            {"type": "field", "inboundTag": ["plain"], "outboundTag": "asis"},
        ]},
    }


def client_config(user):
    return {
        "log": {"loglevel": "warning"},
        "inbounds": [{"tag": "in-" + tag, "listen": LOOP, "port": port, "protocol": "socks", "settings": {"udp": False}} for tag, port in CLIENT_PORTS.items()],
        "outbounds": [{"tag": "out-" + tag, "protocol": "vless", "settings": {"vnext": [{"address": LOOP, "port": NODE_PORTS[tag], "users": [{"id": user, "encryption": "none"}]}]}} for tag in CLIENT_PORTS],
        "routing": {"rules": [{"type": "field", "inboundTag": ["in-" + tag], "outboundTag": "out-" + tag} for tag in CLIENT_PORTS]},
    }


class XrayPair:
    def __init__(self, label, binary, out):
        self.label, self.binary, self.out = label, binary, out
        self.procs = []

    def __enter__(self):
        user = str(uuid.uuid4())
        for role, config in (("node", node_config(user)), ("client", client_config(user))):
            path = os.path.join(self.out, "%s-%s.json" % (self.label, role))
            with open(path, "w") as handle:
                json.dump(config, handle, indent=1)
            log = open(os.path.join(self.out, "%s-%s.log" % (self.label, role)), "w")
            self.procs.append((subprocess.Popen([self.binary, "run", "-c", path], stdout=log, stderr=subprocess.STDOUT), log))
        for port in list(NODE_PORTS.values()) + list(CLIENT_PORTS.values()):
            if not wait_port(port):
                raise SystemExit("%s: port %d never opened" % (self.label, port))
        time.sleep(0.5)
        return self

    def __exit__(self, *exc):
        for proc, log in self.procs:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            log.close()
        return False


class Results:
    def __init__(self):
        self.rows = []
        self.failures = []

    def record(self, build, check, outcome, ok, detail):
        self.rows.append({"build": build, "check": check, "outcome": outcome, "ok": ok, "detail": detail})
        say("[%s] %s %s: %s (%s)" % ("ok" if ok else "FAIL", build, check, outcome, detail))
        if not ok:
            self.failures.append("%s %s: %s (%s)" % (build, check, outcome, detail))


def reached(before, after):
    return after[0] - before[0]


def security_phase(results, label, binary, guarded, out, dns, sinks, link_local):
    private, public_sink, cgnat, link_sink, udp_sink, control_tunnel, rebind_tunnel = sinks

    def judge(check, exposed, detail, base_must_expose=True):
        outcome = "exposed" if exposed else "refused"
        ok = not exposed if guarded else (exposed or not base_must_expose)
        results.record(label, check, outcome, ok, detail)

    step("%s: starting the node and client cores" % label)
    with XrayPair(label, binary, out):
        check = "control: a name of the private listener through the AsIs freedom"
        step(label + " " + check)
        before = private.snapshot()
        took, body, err = socks_get(CLIENT_PORTS["plain"], "private-control.%s" % ZONE, 8080, 8)
        after = private.snapshot()
        judge(check, reached(before, after) > 0, "%d handshakes, %.2fs, %s" % (reached(before, after), took, err or body[:12]))

        check = "A1: a rebinding name (public, then private) through the AsIs freedom"
        step(label + " " + check)
        before = private.snapshot()
        socks_get(CLIENT_PORTS["plain"], "rebind-a1.%s" % ZONE, 8080, 8)
        time.sleep(0.5)
        after = private.snapshot()
        judge(check, reached(before, after) > 0, "%d handshakes, DNS %s" % (reached(before, after), dns.queries("rebind-a1")))

        check = "A3: control, a name of the tunnel's private address through the interface-bound freedom"
        step(label + " " + check)
        before = control_tunnel.packets()
        socks_get(CLIENT_PORTS["india"], "tunnel-a3.%s" % ZONE, control_tunnel.port, 6)
        time.sleep(1.0)
        sent = control_tunnel.packets() - before
        judge(check, sent > 0, "%d TCP packets out of %s toward %s:%d" % (sent, TUNNEL_DEV, TUNNEL_TARGET, control_tunnel.port))

        check = "A2: a rebinding name through the interface-bound freedom, counted for 20 s so a second attempt is seen"
        step(label + " " + check)
        before = rebind_tunnel.packets()
        started = time.time()
        socks_get(CLIENT_PORTS["india"], "rebind-tunnel-a2.%s" % ZONE, rebind_tunnel.port, 6)
        time.sleep(max(0.0, 20.0 - (time.time() - started)))
        sent = rebind_tunnel.packets() - before
        judge(check, sent > 0, "%d TCP packets out of %s toward %s:%d, DNS %s" % (sent, TUNNEL_DEV, TUNNEL_TARGET, rebind_tunnel.port, dns.queries("rebind-tunnel-a2")))

        check = "A4: a name whose first lookup fails, then resolves to the private listener"
        step(label + " " + check)
        before = private.snapshot()
        socks_get(CLIENT_PORTS["plain"], "failfirst-a4.%s" % ZONE, 8080, 8)
        time.sleep(0.5)
        after = private.snapshot()
        judge(check, reached(before, after) > 0, "%d handshakes, DNS %s" % (reached(before, after), dns.queries("failfirst-a4")))

        check = "A5: an open and a closed private port behind rebinding names"
        step(label + " " + check)
        open_took, _, open_err = socks_get(CLIENT_PORTS["plain"], "rebind-a5open.%s" % ZONE, 8080, 10)
        closed_took, _, closed_err = socks_get(CLIENT_PORTS["plain"], "rebind-a5closed.%s" % ZONE, 8090, 10)
        judge(check, abs(closed_took - open_took) > 0.3, "open %.3fs (%s), closed %.3fs (%s)" % (open_took, open_err or "closed", closed_took, closed_err or "closed"))

        if link_local:
            check = "H2: a sniffed Host [[[%s]]] carrying a zoned link-local literal" % link_local
            step(label + " " + check)
            before = link_sink.snapshot()
            socks_get(CLIENT_PORTS["plain"], PUBLIC, 8443, 8, host_header="[[[%s]]]" % link_local)
            time.sleep(0.5)
            after = link_sink.snapshot()
            judge(check, reached(before, after) > 0, "%d handshakes with %s" % (reached(before, after), link_sink.name))
        else:
            results.record(label, "H2: a sniffed zoned link-local Host", "not run", False, "the runner has no IPv6 link-local address")

        check = "U1: UDP-over-TCP (sp.v2.udp-over-tcp.arpa) to the private UDP listener"
        step(label + " " + check)
        before = udp_sink.count()
        frame = b"\x01\x01" + socket.inet_aton(PRIVATE) + struct.pack("!H", 9999) + struct.pack("!H", 8) + b"datagram"
        err = socks_stream(CLIENT_PORTS["plain"], "sp.v2.udp-over-tcp.arpa", 1, frame, 1.0)
        time.sleep(0.5)
        delivered = udp_sink.count() - before
        judge(check, delivered > 0, "%d datagrams at %s %s" % (delivered, udp_sink.name, err))

        check = "B3: the CGNAT literal through rule#2 and DIRECT, the deliberate allow"
        step(label + " " + check)
        took, body, err = socks_get(CLIENT_PORTS["egypt"], CGNAT, 8082, 8)
        results.record(label, check, "served" if b"200 OK" in body else "refused", b"200 OK" in body, "%.2fs %s" % (took, err or body[:12]))

        check = "B4: a public name through the AsIs freedom"
        step(label + " " + check)
        took, body, err = socks_get(CLIENT_PORTS["plain"], "public-b4.%s" % ZONE, 8081, 8)
        results.record(label, check, "served" if b"200 OK" in body else "refused", b"200 OK" in body, "%.2fs %s" % (took, err or body[:12]))

        check = "note: a private name through egypt_wireguard, which no rule allows"
        step(label + " " + check)
        took, body, err = socks_get(CLIENT_PORTS["egypt"], "private-b2.%s" % ZONE, 8080, 8)
        judge(check, b"200 OK" in body, "%.2fs %s" % (took, err or body[:12]), base_must_expose=False)


async def timed_get(port, host):
    start = time.perf_counter()
    reader, writer = await asyncio.open_connection(LOOP, port)
    try:
        writer.write(b"\x05\x01\x00")
        await reader.readexactly(2)
        writer.write(b"\x05\x01\x00" + socks_address(host, 8081))
        head = await reader.readexactly(4)
        await reader.readexactly({1: 6, 4: 18}.get(head[3]) or (await reader.readexactly(1))[0] + 2)
        writer.write(("GET / HTTP/1.0\r\nHost: %s\r\n\r\n" % host).encode())
        writer.write_eof()
        first = await reader.read(1)
        elapsed = time.perf_counter() - start
        rest = await reader.read()
    finally:
        writer.close()
    return elapsed, (first + rest).startswith(b"HTTP/1.0 200")


async def bench(port, names, concurrency):
    gate = asyncio.Semaphore(concurrency)

    async def one(name):
        async with gate:
            try:
                return await asyncio.wait_for(timed_get(port, name), 15)
            except (OSError, asyncio.TimeoutError, asyncio.IncompleteReadError):
                return None, False

    return await asyncio.gather(*(one(name) for name in names))


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(fraction * (len(ordered) - 1))))]


def perf_phase(results, builds, out, dns, plan):
    table = {}
    for label, binary in builds:
        with XrayPair(label + "-perf", binary, out):
            for delay, concurrency, count in plan:
                dns.delay = delay / 1000.0
                prefix = "perf-%s-d%d-c%d-" % (label, delay, concurrency)
                step("perf %s: DNS delay %d ms, concurrency %d, %d requests" % (label, delay, concurrency, count))
                names = ["%s%d.%s" % (prefix, i, ZONE) for i in range(count)]
                timings = asyncio.run(bench(CLIENT_PORTS["plain"], names, concurrency))
                good = [elapsed for elapsed, ok in timings if ok]
                row = {
                    "build": label, "dns_delay_ms": delay, "concurrency": concurrency, "requests": count, "failed": count - len(good),
                    "median_ms": round(1000 * statistics.median(good), 2) if good else None,
                    "p90_ms": round(1000 * percentile(good, 0.90), 2) if good else None,
                    "p99_ms": round(1000 * percentile(good, 0.99), 2) if good else None,
                    "dns_queries_per_request": round(len(dns.queries(prefix)) / count, 2),
                }
                table[(label, delay, concurrency)] = row
                say("PERF", json.dumps(row))
    dns.delay = 0.0
    return table


def judge_perf(results, table, guarded, base):
    for (label, delay, concurrency), row in sorted(table.items()):
        if label != guarded:
            continue
        reference = table[(base, delay, concurrency)]
        check = "latency at DNS delay %d ms, concurrency %d" % (delay, concurrency)
        if row["failed"] or reference["failed"]:
            results.record(label, check, "requests failed", False, "%d failed here, %d on %s" % (row["failed"], reference["failed"], base))
            continue
        same_lookups = abs(row["dns_queries_per_request"] - reference["dns_queries_per_request"]) <= 0.05
        results.record(label, check + ": DNS queries per request", "%.2f vs %.2f on %s" % (row["dns_queries_per_request"], reference["dns_queries_per_request"], base), same_lookups, "the guard may add no lookup")
        if delay > 0 and concurrency == 1:
            slower = row["median_ms"] - reference["median_ms"]
            results.record(label, check + ": median time to the first byte", "%.2f ms vs %.2f ms on %s" % (row["median_ms"], reference["median_ms"], base), slower < delay / 2.0, "an extra lookup would add about %d ms" % delay)


def write_summary(results, table, path):
    lines = ["## Private-target harness", "", "| build | check | outcome | ok | detail |", "|---|---|---|---|---|"]
    for row in results.rows:
        lines.append("| %s | %s | %s | %s | %s |" % (row["build"], row["check"], row["outcome"], "yes" if row["ok"] else "**no**", str(row["detail"]).replace("|", "/")))
    lines += ["", "| build | DNS delay ms | concurrency | requests | failed | median ms | p90 ms | p99 ms | DNS queries per request |", "|---|---|---|---|---|---|---|---|---|"]
    for key in sorted(table):
        row = table[key]
        lines.append("| %s | %d | %d | %d | %d | %s | %s | %s | %s |" % (row["build"], row["dns_delay_ms"], row["concurrency"], row["requests"], row["failed"], row["median_ms"], row["p90_ms"], row["p99_ms"], row["dns_queries_per_request"]))
    text = "\n".join(lines) + "\n"
    print(text, flush=True)
    if path:
        with open(path, "a") as handle:
            handle.write(text)


def first_nameserver(path):
    with open(path) as handle:
        for line in handle:
            parts = line.split()
            if len(parts) >= 2 and parts[0] == "nameserver":
                return parts[1]
    return None


def prepare_runner():
    for address in (PRIVATE, PUBLIC, CGNAT):
        run(["ip", "addr", "add", address + "/32", "dev", "lo"])
    run(["ip", "link", "add", TUNNEL_DEV, "type", "dummy"])
    run(["sysctl", "-w", "net.ipv6.conf.%s.disable_ipv6=1" % TUNNEL_DEV])
    run(["ip", "link", "set", TUNNEL_DEV, "up"])
    run(["ip", "addr", "add", TUNNEL_NET, "dev", TUNNEL_DEV])
    upstream = first_nameserver("/etc/resolv.conf")
    if os.path.islink("/etc/resolv.conf"):
        os.remove("/etc/resolv.conf")
    dns = ScriptedDNS(upstream)
    with open("/etc/resolv.conf", "w") as handle:
        handle.write("nameserver %s\noptions timeout:2 attempts:2\n" % LOOP)
    probe = run(["getent", "hosts", "public-probe." + ZONE], check=False)
    if PUBLIC not in probe.stdout:
        raise SystemExit("the system resolver does not reach the scripted DNS server: %r" % probe.stdout)
    return dns


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--guarded", required=True)
    parser.add_argument("--base", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.geteuid() != 0 or not sys.platform.startswith("linux"):
        raise SystemExit("this harness rewrites the runner's addresses and /etc/resolv.conf: run it as root on a GitHub Actions Linux runner only")
    os.makedirs(args.out, exist_ok=True)
    threading.Thread(target=watchdog, args=(900, args.out), daemon=True).start()
    step("preparing the runner")
    dns = prepare_runner()
    link_local = link_local_address()
    link_sink = Sink(socket.AF_INET6, link_local, 8443) if link_local else None
    sinks = (Sink(socket.AF_INET, PRIVATE, 8080), Sink(socket.AF_INET, PUBLIC, 8081), Sink(socket.AF_INET, CGNAT, 8082), link_sink,
             UDPSink(PRIVATE, 9999), TunnelCounter(8083), TunnelCounter(8084))
    for binary in (args.guarded, args.base):
        run([binary, "version"])
    results = Results()
    security_phase(results, "guarded", args.guarded, True, args.out, dns, sinks, link_local)
    security_phase(results, "base", args.base, False, args.out, dns, sinks, link_local)
    plan = [(0, 1, 30), (0, 32, 128), (20, 1, 30), (20, 32, 128)]
    table = perf_phase(results, [("base", args.base), ("guarded", args.guarded)], args.out, dns, plan)
    judge_perf(results, table, "guarded", "base")
    write_summary(results, table, os.environ.get("GITHUB_STEP_SUMMARY"))
    with open(os.path.join(args.out, "summary.json"), "w") as handle:
        json.dump({"rows": results.rows, "perf": list(table.values()), "failures": results.failures}, handle, indent=1)
    if results.failures:
        print_core_logs(args.out)
        say("%d checks failed:" % len(results.failures))
        for failure in results.failures:
            say("  " + failure)
        sys.exit(1)
    say("every check passed")


if __name__ == "__main__":
    main()
