import argparse
import asyncio
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

PUBLIC = "1.2.3.4"
PRIV_LO = "10.77.0.1"
PRIV_TUN = "10.88.0.5"
TUN_NET = "10.88.0.1/24"
CGNAT = "100.64.0.1"
LOOP = "127.0.0.1"
TUN_DEV = "pgtun0"

BLOCK_V4 = [
    (0, 0, 0, 0, 8), (10, 0, 0, 0, 8), (100, 64, 0, 0, 10), (127, 0, 0, 0, 8), (169, 254, 0, 0, 16),
    (172, 16, 0, 0, 12), (192, 0, 0, 0, 24), (192, 168, 0, 0, 16), (198, 18, 0, 0, 15), (224, 0, 0, 0, 4),
    (240, 0, 0, 0, 4),
]
BLOCK_V6 = ["::/128", "::1/128", "fc00::/7", "fe80::/10"]

TYPE_A = 1
TYPE_AAAA = 28
NXDOMAIN = 3

RESULTS = {"red": [], "breakage": [], "notes": [], "perf": []}


def say(*parts):
    print(time.strftime("%H:%M:%S"), " ".join(str(p) for p in parts), flush=True)


def run(cmd, check=True):
    say("$", " ".join(cmd))
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.stdout.strip():
        say(proc.stdout.strip())
    if proc.stderr.strip():
        say(proc.stderr.strip())
    if check and proc.returncode != 0:
        raise SystemExit("command failed: %s" % " ".join(cmd))
    return proc


def block_cidrs():
    return ["%d.%d.%d.%d/%d" % t for t in BLOCK_V4] + BLOCK_V6


def parse_question(data):
    qid, flags, _, _, _, _ = struct.unpack("!HHHHHH", data[:12])
    pos = 12
    labels = []
    while True:
        length = data[pos]
        pos += 1
        if length == 0:
            break
        labels.append(data[pos:pos + length].decode("ascii", "replace"))
        pos += length
    qtype, _ = struct.unpack("!HH", data[pos:pos + 4])
    return qid, flags, ".".join(labels).lower(), qtype, data[12:pos + 4]


def encode_name(name):
    out = b""
    for label in name.strip(".").split("."):
        out += bytes([len(label)]) + label.encode()
    return out + b"\x00"


def build_reply(qid, flags, question, rcode, ips, ttl, negative_ttl=0):
    rflags = 0x8000 | 0x0400 | (flags & 0x0100) | 0x0080 | (rcode & 0xF)
    authority = b""
    if not ips:
        rdata = encode_name("ns.pgtest") + encode_name("hostmaster.pgtest") + struct.pack("!IIIII", 1, 3600, 600, 86400, negative_ttl)
        authority = encode_name("pgtest") + struct.pack("!HHIH", 6, 1, negative_ttl, len(rdata)) + rdata
    out = struct.pack("!HHHHHH", qid, rflags, 1, len(ips), 1 if authority else 0, 0) + question
    for ip in ips:
        out += struct.pack("!HHHIH", 0xC00C, TYPE_A, 1, ttl, 4) + socket.inet_aton(ip)
    return out + authority


class DNSServer:
    def __init__(self, port, upstream, upstream_port, perf_ttl):
        self.upstream = upstream
        self.upstream_port = upstream_port
        self.perf_ttl = perf_ttl
        self.delay = 0.0
        self.lock = threading.Lock()
        self.a_counts = {}
        self.log = []
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind((LOOP, port))
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                data, peer = self.sock.recvfrom(4096)
            except OSError:
                return
            threading.Thread(target=self.handle, args=(data, peer), daemon=True).start()

    def handle(self, data, peer):
        try:
            qid, flags, name, qtype, question = parse_question(data)
        except Exception:
            return
        if not name.endswith(".pgtest"):
            self.forward(data, peer)
            return
        if self.delay:
            time.sleep(self.delay)
        rcode, ips, ttl = self.decide(name, qtype)
        negative_ttl = self.perf_ttl if (name.endswith(".perf.pgtest") or name.startswith("public")) else 0
        with self.lock:
            self.log.append((time.time(), name, qtype, rcode, tuple(ips)))
        try:
            self.sock.sendto(build_reply(qid, flags, question, rcode, ips, ttl, negative_ttl), peer)
        except OSError:
            pass

    def forward(self, data, peer):
        if not self.upstream:
            return
        up = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        up.settimeout(3)
        try:
            up.sendto(data, (self.upstream, self.upstream_port))
            reply, _ = up.recvfrom(65535)
            self.sock.sendto(reply, peer)
        except OSError:
            pass
        finally:
            up.close()

    def next_a(self, name):
        with self.lock:
            count = self.a_counts.get(name, 0) + 1
            self.a_counts[name] = count
            return count

    def a_seen(self, name):
        with self.lock:
            return self.a_counts.get(name, 0)

    def decide(self, name, qtype):
        label = name[: -len(".pgtest")]
        if qtype not in (TYPE_A, TYPE_AAAA):
            return 0, [], 0
        if label.startswith("failfirst-lo"):
            if qtype == TYPE_A:
                if self.next_a(name) == 1:
                    return NXDOMAIN, [], 0
                return 0, [PRIV_LO], 0
            return (NXDOMAIN if self.a_seen(name) <= 1 else 0), [], 0
        if qtype == TYPE_AAAA:
            return 0, [], 0
        if label.startswith("rebind-lo"):
            return 0, [PUBLIC if self.next_a(name) % 2 == 1 else PRIV_LO], 0
        if label.startswith("rebind-tun"):
            return 0, [PUBLIC if self.next_a(name) % 2 == 1 else PRIV_TUN], 0
        if label.startswith("private-lo"):
            return 0, [PRIV_LO], 0
        if label.startswith("private-tun"):
            return 0, [PRIV_TUN], 0
        if label.startswith("mixed"):
            return 0, [PUBLIC, PRIV_LO], 0
        if label.startswith("cgnat"):
            return 0, [CGNAT], 0
        if label.endswith(".perf") or label.startswith("public"):
            return 0, [PUBLIC], self.perf_ttl
        return NXDOMAIN, [], 0

    def queries_for(self, predicate):
        with self.lock:
            return [entry for entry in self.log if predicate(entry[1])]


class Sink:
    def __init__(self, ip, port):
        self.name = "%s:%d" % (ip, port)
        self.accepts = 0
        self.received = 0
        self.lock = threading.Lock()
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind((ip, port))
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
            threading.Thread(target=self.handle, args=(conn,), daemon=True).start()

    def handle(self, conn):
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


def socks_request(port, host, dport, timeout):
    start = time.perf_counter()
    body = b""
    error = ""
    try:
        sock = socket.create_connection((LOOP, port), timeout=timeout)
    except OSError as exc:
        return time.perf_counter() - start, b"", "connect: %s" % exc
    try:
        sock.settimeout(timeout)
        sock.sendall(b"\x05\x01\x00")
        recv_exact(sock, 2)
        try:
            packed = socket.inet_aton(host)
            sock.sendall(b"\x05\x01\x00\x01" + packed + struct.pack("!H", dport))
        except OSError:
            encoded = host.encode()
            sock.sendall(b"\x05\x01\x00\x03" + bytes([len(encoded)]) + encoded + struct.pack("!H", dport))
        head = recv_exact(sock, 4)
        if head[3] == 1:
            recv_exact(sock, 6)
        elif head[3] == 4:
            recv_exact(sock, 18)
        else:
            recv_exact(sock, recv_exact(sock, 1)[0] + 2)
        sock.sendall(("GET / HTTP/1.0\r\nHost: %s:%d\r\n\r\n" % (host, dport)).encode())
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


LINK_LOCAL = []


class Sink6(Sink):
    def __init__(self, scoped, port):
        self.name = "[%s]:%d" % (scoped, port)
        self.accepts = 0
        self.received = 0
        self.lock = threading.Lock()
        info = socket.getaddrinfo(scoped, port, socket.AF_INET6, socket.SOCK_STREAM)[0][4]
        self.sock = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind(info)
        self.sock.listen(64)
        threading.Thread(target=self.serve, daemon=True).start()


def find_link_local():
    proc = subprocess.run(["ip", "-6", "-o", "addr", "show", "scope", "link"], capture_output=True, text=True)
    for line in proc.stdout.splitlines():
        parts = line.split()
        if len(parts) >= 4 and parts[1] != "lo" and parts[1] != TUN_DEV and parts[3].startswith("fe80"):
            return "%s%%%s" % (parts[3].split("/")[0], parts[1])
    return None


def socks_request_host(port, ip, dport, host_header, timeout):
    start = time.perf_counter()
    body = b""
    error = ""
    try:
        sock = socket.create_connection((LOOP, port), timeout=timeout)
    except OSError as exc:
        return time.perf_counter() - start, b"", "connect: %s" % exc
    try:
        sock.settimeout(timeout)
        sock.sendall(b"\x05\x01\x00")
        recv_exact(sock, 2)
        sock.sendall(b"\x05\x01\x00\x01" + socket.inet_aton(ip) + struct.pack("!H", dport))
        head = recv_exact(sock, 4)
        if head[3] == 1:
            recv_exact(sock, 6)
        elif head[3] == 4:
            recv_exact(sock, 18)
        else:
            recv_exact(sock, recv_exact(sock, 1)[0] + 2)
        sock.sendall(("GET / HTTP/1.1\r\nHost: %s\r\n\r\n" % host_header).encode())
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


def tx_packets(dev):
    with open("/sys/class/net/%s/statistics/tx_packets" % dev) as handle:
        return int(handle.read().strip())


class SynWitness:
    def __init__(self, dev, host):
        self.lines = []
        self.lock = threading.Lock()
        self.proc = subprocess.Popen(["tcpdump", "-i", dev, "-n", "-l", "tcp and dst host %s" % host], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        threading.Thread(target=self.read, daemon=True).start()
        time.sleep(1.0)

    def read(self):
        for line in self.proc.stdout:
            with self.lock:
                self.lines.append(line.strip())

    def count(self):
        with self.lock:
            return len(self.lines), list(self.lines[:6])

    def stop(self):
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()


def wait_port(port, within=20.0):
    deadline = time.time() + within
    while time.time() < deadline:
        try:
            socket.create_connection((LOOP, port), timeout=0.5).close()
            return True
        except OSError:
            time.sleep(0.1)
    return False


def node_config(user_id):
    clients = [{"id": user_id, "email": "hr1@example.test"}]
    sniffing = {"enabled": True, "routeOnly": False, "destOverride": ["http", "tls", "quic"]}
    inbound = lambda tag, port: {"tag": tag, "listen": LOOP, "port": port, "protocol": "vless", "settings": {"clients": clients, "decryption": "none"}, "sniffing": sniffing}
    return {
        "log": {"loglevel": "warning"},
        "dns": {"servers": ["localhost"]},
        "inbounds": [inbound("india", 20001), inbound("egypt", 20002), inbound("plain", 20003)],
        "outbounds": [
            {"tag": "DIRECT", "protocol": "freedom", "settings": {"domainStrategy": "ForceIPv4", "privateAllow": [{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]}},
            {"tag": "india_wireguard", "protocol": "freedom", "streamSettings": {"sockopt": {"interface": TUN_DEV}}},
            {"tag": "egypt_wireguard", "protocol": "freedom", "streamSettings": {"sockopt": {"interface": "lo"}}},
            {"tag": "asis", "protocol": "freedom"},
            {"tag": "BLOCK", "protocol": "blackhole"},
        ],
        "routing": {"domainStrategy": "IPIfNonMatch", "rules": [
            {"type": "field", "port": "25", "outboundTag": "BLOCK"},
            {"type": "field", "ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"], "outboundTag": "DIRECT"},
            {"type": "field", "ip": block_cidrs(), "outboundTag": "BLOCK"},
            {"type": "field", "inboundTag": ["india"], "outboundTag": "india_wireguard"},
            {"type": "field", "inboundTag": ["egypt"], "outboundTag": "egypt_wireguard"},
            {"type": "field", "inboundTag": ["plain"], "outboundTag": "asis"},
        ]},
    }


def client_config(user_id):
    pairs = [("s1", 10801, "o1", 20001), ("s2", 10802, "o2", 20002), ("s3", 10803, "o3", 20003)]
    return {
        "log": {"loglevel": "warning"},
        "inbounds": [{"tag": s, "listen": LOOP, "port": p, "protocol": "socks", "settings": {"udp": False}} for s, p, _, _ in pairs],
        "outbounds": [{"tag": o, "protocol": "vless", "settings": {"vnext": [{"address": LOOP, "port": q, "users": [{"id": user_id, "encryption": "none"}]}]}} for _, _, o, q in pairs],
        "routing": {"rules": [{"type": "field", "inboundTag": [s], "outboundTag": o} for s, _, o, _ in pairs]},
    }


class Pair:
    def __init__(self, label, binary, out):
        self.label = label
        self.binary = binary
        self.out = out
        self.procs = []

    def __enter__(self):
        user_id = str(uuid.uuid4())
        for role, config in (("node", node_config(user_id)), ("client", client_config(user_id))):
            path = os.path.join(self.out, "%s-%s.json" % (self.label, role))
            with open(path, "w") as handle:
                json.dump(config, handle, indent=1)
            log = open(os.path.join(self.out, "%s-%s.log" % (self.label, role)), "w")
            proc = subprocess.Popen([self.binary, "run", "-c", path], stdout=log, stderr=subprocess.STDOUT)
            self.procs.append((proc, log))
        for port in (20001, 20002, 20003, 10801, 10802, 10803):
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


def flag(kind, text):
    RESULTS[kind].append(text)
    say("[%s] %s" % (kind.upper(), text))


def security_phase(label, binary, out, sinks, dns, guarded):
    private, private_8081, public, cgnat = sinks
    with Pair(label, binary, out):
        say("== %s: control, a name that always resolves to the private listener, through the AsIs freedom" % label)
        before = private.snapshot()
        took, body, err = socks_request(10803, "private-lo-control.pgtest", 8080, 8)
        after = private.snapshot()
        say("   took %.3fs body=%r err=%s private accepts %d->%d bytes %d->%d" % (took, body[:40], err, before[0], after[0], before[1], after[1]))
        if guarded and after[0] != before[0]:
            flag("red", "%s: the guard let a plain private name reach the listener" % label)

        say("== %s: A1 rebinding name (public then private) through the AsIs freedom" % label)
        before = private.snapshot()
        took, body, err = socks_request(10803, "rebind-lo-a1.pgtest", 8080, 8)
        time.sleep(0.5)
        after = private.snapshot()
        say("   took %.3fs body=%r err=%s private accepts %d->%d bytes %d->%d" % (took, body[:40], err, before[0], after[0], before[1], after[1]))
        say("   dns:", [(q[1], q[2], q[3], q[4]) for q in dns.queries_for(lambda n: n.startswith("rebind-lo-a1"))])
        if guarded and after[0] > before[0]:
            flag("red", "%s A1: a rebinding name completed %d TCP handshakes with the private listener %s (bytes %d)" % (label, after[0] - before[0], private.name, after[1] - before[1]))

        say("== %s: A3 control, a name that always resolves to the tunnel's private address" % label)
        witness = SynWitness(TUN_DEV, PRIV_TUN)
        before = tx_packets(TUN_DEV)
        took, body, err = socks_request(10801, "private-tun-a3.pgtest", 8080, 6)
        time.sleep(1.0)
        after = tx_packets(TUN_DEV)
        seen, sample = witness.count()
        witness.stop()
        say("   took %.3fs body=%r err=%s %s tx_packets %d->%d, tcpdump packets to %s: %d %s" % (took, body[:40], err, TUN_DEV, before, after, PRIV_TUN, seen, sample))
        if guarded and seen > 0:
            flag("red", "%s A3: the guard let a plain private name into the tunnel" % label)

        say("== %s: A2 rebinding name through the interface-bound freedom (%s, a dummy tunnel)" % (label, TUN_DEV))
        witness = SynWitness(TUN_DEV, PRIV_TUN)
        before = tx_packets(TUN_DEV)
        took, body, err = socks_request(10801, "rebind-tun-a2.pgtest", 8080, 6)
        time.sleep(1.0)
        after = tx_packets(TUN_DEV)
        seen, sample = witness.count()
        witness.stop()
        say("   took %.3fs body=%r err=%s %s tx_packets %d->%d, tcpdump packets to %s: %d %s" % (took, body[:40], err, TUN_DEV, before, after, PRIV_TUN, seen, sample))
        say("   dns:", [(q[1], q[2], q[3], q[4]) for q in dns.queries_for(lambda n: n.startswith("rebind-tun-a2"))])
        if guarded and seen > 0:
            flag("red", "%s A2: a rebinding name sent %d TCP packets into the interface-bound tunnel %s toward %s (tx_packets +%d)" % (label, seen, TUN_DEV, PRIV_TUN, after - before))

        say("== %s: A4 a name whose first lookup fails, then resolves to the private listener" % label)
        before = private.snapshot()
        took, body, err = socks_request(10803, "failfirst-lo-a4.pgtest", 8080, 8)
        time.sleep(0.5)
        after = private.snapshot()
        say("   took %.3fs body=%r err=%s private accepts %d->%d" % (took, body[:40], err, before[0], after[0]))
        say("   dns:", [(q[1], q[2], q[3], q[4]) for q in dns.queries_for(lambda n: n.startswith("failfirst-lo-a4"))])
        if guarded and after[0] > before[0]:
            flag("red", "%s A4: a failing first lookup let %d handshakes reach the private listener" % (label, after[0] - before[0]))

        say("== %s: A5 port-scan oracle, rebinding names to an open and a closed private port" % label)
        open_took, open_body, open_err = socks_request(10803, "rebind-lo-a5open.pgtest", 8080, 10)
        closed_took, closed_body, closed_err = socks_request(10803, "rebind-lo-a5closed.pgtest", 8090, 10)
        say("   open  port 8080: %.3fs body=%r err=%s" % (open_took, open_body[:40], open_err))
        say("   closed port 8090: %.3fs body=%r err=%s" % (closed_took, closed_body[:40], closed_err))
        if guarded and closed_took - open_took > 0.3:
            flag("red", "%s A5: an open private port closes in %.3fs and a closed one in %.3fs, seen by the user" % (label, open_took, closed_took))

        if LINK_LOCAL:
            say("== %s: H2 a sniffed Host [[[%s]]] smuggles a zoned link-local literal past the pre-dial lookup" % (label, LINK_LOCAL[0]))
            sink6 = LINK_LOCAL[1]
            before = sink6.snapshot()
            took, body, err = socks_request_host(10803, PUBLIC, 8443, "[[[%s]]]" % LINK_LOCAL[0], 8)
            time.sleep(0.5)
            after = sink6.snapshot()
            say("   took %.3fs body=%r err=%s link-local accepts %d->%d bytes %d->%d" % (took, body[:40], err, before[0], after[0], before[1], after[1]))
            if guarded and after[0] > before[0]:
                flag("red", "%s H2: a sniffed bracketed zoned Host completed %d TCP handshakes with the link-local listener [%s]:8443 (bytes %d)" % (label, after[0] - before[0], LINK_LOCAL[0], after[1] - before[1]))

        say("== %s: B1 a public name that also has a private A record" % label)
        took, body, err = socks_request(10803, "mixed-b1.pgtest", 8081, 8)
        say("   took %.3fs body=%r err=%s" % (took, body[:60], err))
        RESULTS["notes"].append("%s B1 mixed public+private name: %s" % (label, "served" if b"200 OK" in body else "refused (%s)" % (err or "closed")))

        say("== %s: B2 a CGNAT name through the interface-bound egypt_wireguard (core 1 inbound rule)" % label)
        took, body, err = socks_request(10802, "cgnat-b2.pgtest", 8082, 8)
        say("   took %.3fs body=%r err=%s" % (took, body[:60], err))
        RESULTS["notes"].append("%s B2 CGNAT name via egypt_wireguard: %s" % (label, "served" if b"200 OK" in body else "refused (%s)" % (err or "closed")))

        say("== %s: B3 a CGNAT literal through rule#2 and DIRECT (the deliberate allow)" % label)
        took, body, err = socks_request(10802, CGNAT, 8082, 8)
        say("   took %.3fs body=%r err=%s" % (took, body[:60], err))
        RESULTS["notes"].append("%s B3 CGNAT literal via DIRECT: %s" % (label, "served" if b"200 OK" in body else "refused (%s)" % (err or "closed")))
        if guarded and b"200 OK" not in body:
            flag("breakage", "%s B3: the deliberate CGNAT route through DIRECT was refused" % label)

        say("== %s: B4 a public name through the AsIs freedom" % label)
        took, body, err = socks_request(10803, "public-b4.pgtest", 8081, 8)
        say("   took %.3fs body=%r err=%s" % (took, body[:60], err))
        if b"200 OK" not in body:
            flag("breakage", "%s B4: a public name was not served" % label)


async def one_request(port, host, dport):
    start = time.perf_counter()
    reader, writer = await asyncio.open_connection(LOOP, port)
    try:
        writer.write(b"\x05\x01\x00")
        await writer.drain()
        await reader.readexactly(2)
        encoded = host.encode()
        writer.write(b"\x05\x01\x00\x03" + bytes([len(encoded)]) + encoded + struct.pack("!H", dport))
        await writer.drain()
        head = await reader.readexactly(4)
        if head[3] == 1:
            await reader.readexactly(6)
        elif head[3] == 4:
            await reader.readexactly(18)
        else:
            await reader.readexactly((await reader.readexactly(1))[0] + 2)
        writer.write(("GET / HTTP/1.0\r\nHost: %s\r\n\r\n" % host).encode())
        await writer.drain()
        first = await reader.read(1)
        first_byte = time.perf_counter() - start
        data = first + await reader.read()
    finally:
        writer.close()
    return first_byte, data.startswith(b"HTTP/1.0 200")


async def bench(port, count, concurrency, prefix):
    gate = asyncio.Semaphore(concurrency)

    async def guarded(index):
        async with gate:
            try:
                return await asyncio.wait_for(one_request(port, "%s%d.perf.pgtest" % (prefix, index), 8081), 12)
            except Exception:
                return None, False

    started = time.perf_counter()
    results = await asyncio.gather(*(guarded(i) for i in range(count)))
    return time.perf_counter() - started, results


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(round(fraction * (len(ordered) - 1))))]


def perf_phase(resolver, binaries, out, dns, delays, plan):
    for delay in delays:
        dns.delay = delay / 1000.0
        for concurrency, count in plan:
            for label, binary in binaries:
                prefix = "%s-%s-d%d-c%d-" % (resolver, label, delay, concurrency)
                with Pair("%s-perf" % label, binary, out):
                    probe_wall, probe = asyncio.run(bench(10803, 5, 5, prefix + "probe-"))
                    probe_ok = sum(1 for t, good in probe if good)
                    if probe_ok < 4:
                        say("PERF probe failed for", prefix, "ok", probe_ok, "of 5 in %.1fs; run skipped" % probe_wall)
                        RESULTS["notes"].append("perf %s skipped: probe %d of 5" % (prefix, probe_ok))
                        continue
                    wall, results = asyncio.run(bench(10803, count, concurrency, prefix))
                ok = [t for t, good in results if good and t is not None]
                failed = len(results) - len(ok)
                queries = len(dns.queries_for(lambda n, p=prefix: n.startswith(p)))
                row = {
                    "resolver": resolver, "binary": label, "dns_delay_ms": delay, "concurrency": concurrency, "requests": count,
                    "failed": failed, "wall_s": round(wall, 3),
                    "metric": "time to first response byte",
                    "median_ms": round(1000 * statistics.median(ok), 2) if ok else None,
                    "p90_ms": round(1000 * percentile(ok, 0.90), 2) if ok else None,
                    "p99_ms": round(1000 * percentile(ok, 0.99), 2) if ok else None,
                    "dns_queries_per_request": round(queries / count, 2),
                }
                RESULTS["perf"].append(row)
                say("PERF", json.dumps(row))
    dns.delay = 0.0


def set_resolv(content):
    if os.path.islink("/etc/resolv.conf") or os.path.exists("/etc/resolv.conf"):
        os.remove("/etc/resolv.conf")
    with open("/etc/resolv.conf", "w") as handle:
        handle.write(content)
    say("resolv.conf now:", content.replace("\n", " | "))


def first_nameserver(path):
    with open(path) as handle:
        for line in handle:
            parts = line.split()
            if len(parts) >= 2 and parts[0] == "nameserver":
                return parts[1]
    return None


ORIGINAL_RESOLV = []


def remember_resolv():
    path = "/etc/resolv.conf"
    if os.path.islink(path):
        ORIGINAL_RESOLV.extend(["link", os.readlink(path)])
    else:
        with open(path) as handle:
            ORIGINAL_RESOLV.extend(["file", handle.read()])


def restore_resolv():
    if not ORIGINAL_RESOLV:
        return
    path = "/etc/resolv.conf"
    if os.path.islink(path) or os.path.exists(path):
        os.remove(path)
    if ORIGINAL_RESOLV[0] == "link":
        os.symlink(ORIGINAL_RESOLV[1], path)
    else:
        with open(path, "w") as handle:
            handle.write(ORIGINAL_RESOLV[1])
    say("resolv.conf restored")


def main():
    remember_resolv()
    try:
        harness()
    finally:
        restore_resolv()


def harness():
    parser = argparse.ArgumentParser()
    parser.add_argument("--guard", required=True)
    parser.add_argument("--base", required=True)
    parser.add_argument("--fix", default="")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    os.makedirs(args.out, exist_ok=True)

    for addr in (PRIV_LO, PUBLIC, CGNAT):
        run(["ip", "addr", "add", addr + "/32", "dev", "lo"])
    run(["ip", "link", "add", TUN_DEV, "type", "dummy"])
    run(["sysctl", "-w", "net.ipv6.conf.%s.disable_ipv6=1" % TUN_DEV])
    run(["ip", "link", "set", TUN_DEV, "up"])
    run(["ip", "addr", "add", TUN_NET, "dev", TUN_DEV])
    run(["ip", "-4", "route", "show"])
    time.sleep(2)

    stub_path = "/run/systemd/resolve/stub-resolv.conf"
    uplink_path = "/run/systemd/resolve/resolv.conf"
    with open(stub_path) as handle:
        stub_content = handle.read()
    stub_ns = first_nameserver(stub_path)
    uplink_ns = first_nameserver(uplink_path)
    say("systemd-resolved stub", stub_ns, "uplink present:", bool(uplink_ns))

    sinks = (Sink(PRIV_LO, 8080), Sink(PRIV_LO, 8081), Sink(PUBLIC, 8081), Sink(CGNAT, 8082))
    scoped = find_link_local()
    say("link-local address for H2:", scoped)
    if scoped:
        try:
            LINK_LOCAL.extend([scoped, Sink6(scoped, 8443)])
        except OSError as exc:
            say("no link-local listener:", exc)
            LINK_LOCAL.clear()
    direct_dns = DNSServer(53, stub_ns, 53, 0)
    set_resolv("nameserver %s\noptions timeout:2 attempts:2\n" % LOOP)
    run(["getent", "hosts", "public-check.pgtest"], check=False)
    run(["getent", "hosts", "github.com"], check=False)

    for binary in [args.guard, args.base] + ([args.fix] if args.fix else []):
        run([binary, "version"])
    phases = [("guard", args.guard, True), ("base", args.base, False)]
    if args.fix:
        phases.append(("fixv2", args.fix, True))
    for label, binary, guarded in phases:
        say("######## security phase, non-caching system resolver, %s" % label)
        security_phase(label, binary, args.out, sinks, direct_dns, guarded)

    say("######## performance phase, non-caching system resolver")
    perf_binaries = [("base", args.base), ("guard", args.guard)] + ([("fixv2", args.fix)] if args.fix else [])
    perf_phase("direct", perf_binaries, args.out, direct_dns, (0, 20), ((1, 40), (32, 256)))

    say("######## systemd-resolved caching stub as the system resolver")
    sd_dns = DNSServer(5354, uplink_ns, 53, 60)
    os.makedirs("/etc/systemd/resolved.conf.d", exist_ok=True)
    with open("/etc/systemd/resolved.conf.d/hr1.conf", "w") as handle:
        handle.write("[Resolve]\nDNS=%s:5354\nDomains=~pgtest\nDNSSEC=no\nCache=yes\nCacheFromLocalhost=yes\n" % LOOP)
    run(["systemctl", "restart", "systemd-resolved"])
    time.sleep(2)
    set_resolv(stub_content)
    run(["resolvectl", "status"], check=False)
    check = run(["getent", "hosts", "public-check.pgtest"], check=False)
    run(["getent", "hosts", "github.com"], check=False)
    if PUBLIC not in check.stdout:
        say("systemd-resolved does not route pgtest to the test server; resolved phase skipped")
        RESULTS["notes"].append("resolved phase skipped: getent gave %r" % check.stdout.strip())
    else:
        say("######## security phase through systemd-resolved, guard")
        security_phase("guard-resolved", args.guard, args.out, sinks, sd_dns, True)
        if args.fix:
            security_phase("fixv2-resolved", args.fix, args.out, sinks, sd_dns, True)
        say("######## performance phase through systemd-resolved (perf names TTL 60)")
        perf_phase("resolved", perf_binaries, args.out, sd_dns, (20,), ((1, 40), (32, 256)))

    say("######## SUMMARY")
    say(json.dumps(RESULTS, indent=1))
    with open(os.path.join(args.out, "summary.json"), "w") as handle:
        json.dump(RESULTS, handle, indent=1)
    by_build = {}
    for line in RESULTS["red"]:
        by_build.setdefault(line.split(" ", 1)[0], []).append(line)
    for build in sorted(by_build):
        say("RED on %s: %d" % (build, len(by_build[build])))
    if args.fix and not any(b.startswith("fixv2") for b in by_build):
        say("GREEN on fixv2: no security assertion failed")
    if RESULTS["red"]:
        sys.exit(1)
    say("GREEN: no security assertion failed")


if __name__ == "__main__":
    main()
