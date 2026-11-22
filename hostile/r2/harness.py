import argparse
import faulthandler
import json
import os
import signal
import socket
import statistics
import struct
import subprocess
import sys
import threading
import time
import uuid

LOOP = "127.0.0.1"
PRIVATE = "10.66.0.1"
CGNAT = "100.66.0.1"
PUBLIC = "1.2.3.4"
LAN_DEV = "lo"
TUN_DEV = "pgr2tun"
TUN_NET = "10.99.0.1/24"
TUN_TARGET = "10.99.0.5"
ZONE = "r2test"
QUIC_NAME = "www.google.com"
UOT_NAMES = ("sp.v2.udp-over-tcp.arpa", "sp.udp-over-tcp.arpa")
RESOLV = "/etc/resolv.conf"
TYPE_A, TYPE_AAAA = 1, 28
RCODE_OK, RCODE_NXDOMAIN = 0, 3
PRIVATE_CIDRS = [
    "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
    "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
    "203.0.113.0/24", "224.0.0.0/3", "::/127", "fc00::/7", "fe80::/10", "ff00::/8",
]
NODE_PORTS = {"users": 21001, "wg": 21002, "lan": 21003, "direct": 21004}
CLIENT_PORTS = {"users": 11001, "wg": 11002, "lan": 11003, "direct": 11004}
CLEANUPS = []
CURRENT = ["starting"]
PROCS = []


def say(*parts):
    print(time.strftime("%H:%M:%S"), " ".join(str(p) for p in parts), flush=True)


def step(text):
    CURRENT[0] = text
    say("==", text)


def run(cmd, check=True):
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if check and proc.returncode != 0:
        raise SystemExit("command failed: %s: %s %s" % (" ".join(cmd), proc.stdout, proc.stderr))
    return proc


def watchdog(budget):
    started = time.time()
    while True:
        time.sleep(30)
        elapsed = time.time() - started
        say("watchdog: %.0f s, at %s" % (elapsed, CURRENT[0]))
        if elapsed > budget:
            while CLEANUPS:
                CLEANUPS.pop()()
            faulthandler.dump_traceback(all_threads=True)
            os._exit(3)


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
        self.lock = threading.Lock()
        self.asked = {}
        self.log = []
        self.delay = 0.0
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
        if not name.endswith("." + ZONE) and name != QUIC_NAME and name not in UOT_NAMES:
            self.forward(packet, peer)
            return
        with self.lock:
            key = (name, qtype)
            self.asked[key] = self.asked.get(key, 0) + 1
            lookup = self.asked[key]
            rcode, addresses = self.decide(name, qtype, lookup)
            self.log.append((name, qtype, lookup, rcode, tuple(addresses)))
        if self.delay and name.startswith("perf-"):
            time.sleep(self.delay)
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

    @staticmethod
    def decide(name, qtype, lookup):
        if name == QUIC_NAME:
            return RCODE_OK, [PRIVATE] if qtype == TYPE_A else []
        if not name.endswith("." + ZONE):
            return RCODE_NXDOMAIN, []
        label = name.split(".", 1)[0]
        table = (
            ("private-", [PRIVATE], []),
            ("cgnat-", [CGNAT], []),
            ("public-", [PUBLIC], []),
            ("perf-", [PUBLIC], []),
            ("mixed-", [PUBLIC, PRIVATE], []),
            ("tunnel-", [TUN_TARGET], []),
            ("v4compat-", [], ["::" + PRIVATE]),
            ("nat64-", [], ["64:ff9b::" + PRIVATE]),
            ("dual-", [PRIVATE], ["2001:db8::1"]),
        )
        if label.startswith("rebind-tun-"):
            if qtype != TYPE_A:
                return RCODE_OK, []
            return RCODE_OK, [PUBLIC if lookup % 2 == 1 else TUN_TARGET]
        for prefix, a, aaaa in table:
            if label.startswith(prefix):
                return RCODE_OK, a if qtype == TYPE_A else (aaaa if qtype == TYPE_AAAA else [])
        return RCODE_NXDOMAIN, []

    def queries(self, prefix):
        with self.lock:
            return [entry for entry in self.log if entry[0].startswith(prefix)]


class TCPSink:
    def __init__(self, address, port):
        self.name = "%s:%d" % (address, port)
        self.lock = threading.Lock()
        self.accepts = 0
        self.received = 0
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.sock.bind((address, port))
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


class UDPSink:
    def __init__(self, address, port):
        self.name = "%s:%d" % (address, port)
        self.lock = threading.Lock()
        self.datagrams = 0
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 8 << 20)
        self.sock.bind((address, port))
        threading.Thread(target=self.serve, daemon=True).start()

    def serve(self):
        while True:
            try:
                self.sock.recvfrom(65535)
            except OSError:
                return
            with self.lock:
                self.datagrams += 1

    def count(self):
        with self.lock:
            return self.datagrams


class TunCounter:
    def __init__(self, proto):
        self.proto = proto
        self.comment = "hr2-" + proto
        run(["iptables", "-w", "-I", "OUTPUT", "1", "-o", TUN_DEV, "-d", TUN_TARGET + "/32", "-p", proto, "-m", "comment", "--comment", self.comment])
        CLEANUPS.append(lambda: run(["iptables", "-w", "-D", "OUTPUT", "-o", TUN_DEV, "-d", TUN_TARGET + "/32", "-p", proto, "-m", "comment", "--comment", self.comment], check=False))

    def packets(self):
        listing = run(["iptables", "-w", "-L", "OUTPUT", "-v", "-x", "-n"]).stdout
        for line in listing.splitlines():
            if "/* %s */" % self.comment in line:
                return int(line.split()[0])
        raise SystemExit("the iptables counter %s is gone:\n%s" % (self.comment, listing))


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


def socks_connect(socks_port, host, port, timeout):
    sock = socket.create_connection((LOOP, socks_port), timeout=timeout)
    sock.settimeout(timeout)
    sock.sendall(b"\x05\x01\x00")
    recv_exact(sock, 2)
    sock.sendall(b"\x05\x01\x00" + socks_address(host, port))
    head = recv_exact(sock, 4)
    recv_exact(sock, {1: 6, 4: 18}.get(head[3]) or recv_exact(sock, 1)[0] + 2)
    return sock


def socks_get(socks_port, host, port, timeout=8):
    start = time.perf_counter()
    first, body, error = None, b"", ""
    try:
        sock = socks_connect(socks_port, host, port, timeout)
    except OSError as exc:
        return time.perf_counter() - start, None, b"", "socks: %s" % exc
    try:
        sock.sendall(("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n" % host).encode())
        sock.shutdown(socket.SHUT_WR)
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            if first is None:
                first = time.perf_counter() - start
            body += chunk
            if b"\r\n\r\nok" in body:
                break
    except OSError as exc:
        error = str(exc) or exc.__class__.__name__
    finally:
        sock.close()
    return time.perf_counter() - start, first, body, error


class SocksUDP:
    def __init__(self, socks_port):
        self.ctrl = socket.create_connection((LOOP, socks_port), timeout=8)
        self.ctrl.sendall(b"\x05\x01\x00")
        recv_exact(self.ctrl, 2)
        self.ctrl.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
        head = recv_exact(self.ctrl, 4)
        if head[1] != 0:
            raise OSError("UDP ASSOCIATE refused: %d" % head[1])
        rest = recv_exact(self.ctrl, 6)
        self.relay = (socket.inet_ntoa(rest[:4]), struct.unpack("!H", rest[4:])[0])
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind((LOOP, 0))
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 8 << 20)

    def send(self, host, port, payload):
        self.sock.sendto(b"\x00\x00\x00" + socks_address(host, port) + payload, self.relay)

    def close(self):
        self.sock.close()
        self.ctrl.close()


def uot_connect_frame(host, port, payload):
    request = b"\x01" + b"\x01" + socket.inet_aton(host) + struct.pack("!H", port)
    return request + struct.pack("!H", len(payload)) + payload


def node_config(user):
    sniffing = {"enabled": True, "routeOnly": False, "destOverride": ["http", "tls", "quic"]}

    def inbound(tag):
        return {"tag": tag, "listen": LOOP, "port": NODE_PORTS[tag], "protocol": "vless", "sniffing": sniffing,
                "settings": {"clients": [{"id": user, "email": "r2@example.test"}], "decryption": "none"}}

    return {
        "log": {"loglevel": "warning"},
        "dns": {"servers": ["localhost"], "queryStrategy": "UseIPv4"},
        "inbounds": [inbound(tag) for tag in NODE_PORTS],
        "outbounds": [
            {"tag": "DIRECT", "protocol": "freedom", "settings": {"domainStrategy": "ForceIPv4", "privateAllow": [{"ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"]}]}},
            {"tag": "tun_wireguard", "protocol": "freedom", "settings": {}, "streamSettings": {"sockopt": {"interface": TUN_DEV}}},
            {"tag": "lan_wireguard", "protocol": "freedom", "settings": {}, "streamSettings": {"sockopt": {"interface": LAN_DEV}}},
            {"tag": "BLOCK", "protocol": "blackhole", "settings": {}},
        ],
        "routing": {"domainStrategy": "IPIfNonMatch", "rules": [
            {"type": "field", "port": "25", "outboundTag": "BLOCK"},
            {"type": "field", "network": "udp", "port": "443", "ip": ["1.1.1.1/32"], "outboundTag": "BLOCK"},
            {"type": "field", "ruleTag": "PG_NODE_PRIVATE_ALLOW_1", "ip": ["100.64.0.0/10"], "outboundTag": "DIRECT"},
            {"type": "field", "ip": PRIVATE_CIDRS, "outboundTag": "BLOCK"},
            {"type": "field", "inboundTag": ["wg"], "outboundTag": "tun_wireguard"},
            {"type": "field", "inboundTag": ["lan"], "outboundTag": "lan_wireguard"},
            {"type": "field", "inboundTag": ["direct"], "outboundTag": "DIRECT"},
        ]},
    }


def client_config(user):
    inbounds, outbounds, rules = [], [], []
    for tag, port in CLIENT_PORTS.items():
        inbounds.append({"tag": "in-" + tag, "listen": LOOP, "port": port, "protocol": "socks", "settings": {"udp": True, "ip": LOOP}})
        outbounds.append({"tag": "out-" + tag, "protocol": "vless", "settings": {"vnext": [{"address": LOOP, "port": NODE_PORTS[tag], "users": [{"id": user, "encryption": "none"}]}]}})
        rules.append({"type": "field", "inboundTag": ["in-" + tag], "outboundTag": "out-" + tag})
    return {"log": {"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": {"rules": rules}}


def wait_port(port, within=20.0):
    deadline = time.time() + within
    while time.time() < deadline:
        try:
            socket.create_connection((LOOP, port), timeout=0.5).close()
            return True
        except OSError:
            time.sleep(0.1)
    return False


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
            proc = subprocess.Popen([self.binary, "run", "-c", path], stdout=log, stderr=subprocess.STDOUT)
            PROCS.append(proc)
            self.procs.append((proc, log))
        for port in list(NODE_PORTS.values()) + list(CLIENT_PORTS.values()):
            if not wait_port(port):
                raise SystemExit("%s: port %d never opened" % (self.label, port))
        time.sleep(0.5)
        return self

    def node_pid(self):
        return self.procs[0][0].pid

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


def cpu_seconds(pid):
    with open("/proc/%d/stat" % pid) as handle:
        fields = handle.read().rsplit(")", 1)[1].split()
    return (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK")


class Results:
    def __init__(self):
        self.rows = []
        self.failures = []
        self.findings = []

    def record(self, build, check, outcome, ok, detail, finding=False):
        self.rows.append({"build": build, "check": check, "outcome": outcome, "ok": ok, "detail": detail})
        tag = "ok" if ok else ("RED" if finding else "FAIL")
        say("[%s] %s %s: %s (%s)" % (tag, build, check, outcome, detail))
        if not ok:
            (self.findings if finding else self.failures).append("%s %s: %s (%s)" % (build, check, outcome, detail))


def security_phase(results, label, binary, guarded, out, dns, sinks, counters):
    tcp_private, tcp_cgnat, tcp_public, udp_private, udp_cgnat, udp_public, udp_quic = sinks
    tun_tcp, tun_udp = counters

    def expect(check, exposed, detail, base_exposes=True, finding=False):
        outcome = "exposed" if exposed else "refused"
        ok = (not exposed) if guarded else (exposed or not base_exposes)
        results.record(label, check, outcome, ok, detail, finding)

    def expect_served(check, served, detail, finding=False):
        results.record(label, check, "served" if served else "not served", served, detail, finding)

    step("%s: starting node and client" % label)
    with XrayPair(label, binary, out) as pair:
        check = "X1: XUDP FullCone, a public datagram then a private one through the AsIs lan_wireguard"
        step(label + " " + check)
        before_pub, before_priv = udp_public.count(), udp_private.count()
        flow = SocksUDP(CLIENT_PORTS["lan"])
        for _ in range(3):
            flow.send(PUBLIC, 5353, b"public")
            time.sleep(0.2)
            flow.send(PRIVATE, 5353, b"private")
            time.sleep(0.2)
        time.sleep(1.0)
        flow.close()
        pub, priv = udp_public.count() - before_pub, udp_private.count() - before_priv
        expect(check, priv > 0, "public sink %d, private sink %d datagrams" % (pub, priv))

        check = "X2: XUDP FullCone through DIRECT (ForceIPv4), public then private"
        step(label + " " + check)
        before_pub, before_priv = udp_public.count(), udp_private.count()
        flow = SocksUDP(CLIENT_PORTS["direct"])
        for _ in range(3):
            flow.send(PUBLIC, 5353, b"public")
            time.sleep(0.2)
            flow.send(PRIVATE, 5353, b"private")
            time.sleep(0.2)
        time.sleep(1.0)
        flow.close()
        pub, priv = udp_public.count() - before_pub, udp_private.count() - before_priv
        expect(check, priv > 0, "public sink %d, private sink %d datagrams" % (pub, priv))

        check = "Q1: a QUIC Initial to %s:443 whose SNI %s the node resolves to %s, through lan_wireguard" % (PUBLIC, QUIC_NAME, PRIVATE)
        step(label + " " + check)
        before = udp_quic.count()
        flow = SocksUDP(CLIENT_PORTS["lan"])
        for _ in range(3):
            flow.send(PUBLIC, 443, QUIC_INITIAL)
            time.sleep(0.3)
        time.sleep(1.5)
        flow.close()
        got = udp_quic.count() - before
        expect(check, got > 0, "private QUIC sink %d datagrams, DNS %s" % (got, dns.queries(QUIC_NAME)[-2:]))

        check = "U1: UDP-over-TCP to the private UDP sink through the AsIs lan_wireguard"
        step(label + " " + check)
        before = udp_private.count()
        try:
            sock = socks_connect(CLIENT_PORTS["lan"], UOT_NAMES[0], 0, 8)
            sock.sendall(uot_connect_frame(PRIVATE, 5353, b"uot-datagram"))
            time.sleep(1.5)
            sock.close()
            err = ""
        except OSError as exc:
            err = str(exc)
        got = udp_private.count() - before
        expect(check, got > 0, "private sink %d datagrams %s" % (got, err))

        check = "U2: UDP-over-TCP to the private UDP sink through DIRECT (ForceIPv4), as on cores 1/17/19/21"
        step(label + " " + check)
        before = udp_private.count()
        try:
            sock = socks_connect(CLIENT_PORTS["direct"], UOT_NAMES[0], 0, 8)
            sock.sendall(uot_connect_frame(PRIVATE, 5353, b"uot-datagram"))
            time.sleep(2.5)
            sock.close()
            err = ""
        except OSError as exc:
            err = str(exc)
        got = udp_private.count() - before
        results.record(label, check, "delivered %d" % got, True, "informational: whether ForceIPv4 DIRECT ever carried UoT; %s" % err)

        check = "T1: a rebinding name (public, then the tunnel target) through the interface-bound tun_wireguard, SYNs counted"
        step(label + " " + check)
        before = tun_tcp.packets()
        socks_get(CLIENT_PORTS["wg"], "rebind-tun-t1.%s" % ZONE, 8080, 6)
        time.sleep(1.0)
        socks_get(CLIENT_PORTS["wg"], "rebind-tun-t1.%s" % ZONE, 8080, 6)
        time.sleep(1.0)
        syns = tun_tcp.packets() - before
        expect(check, syns > 0, "%d packets to %s into %s, DNS %s" % (syns, TUN_TARGET, TUN_DEV, dns.queries("rebind-tun-t1")))

        check = "T2: a UDP name of the tunnel target through tun_wireguard"
        step(label + " " + check)
        before = tun_udp.packets()
        flow = SocksUDP(CLIENT_PORTS["wg"])
        flow.send("tunnel-t2.%s" % ZONE, 5353, b"udp-to-tunnel")
        time.sleep(1.0)
        flow.close()
        sent = tun_udp.packets() - before
        expect(check, sent > 0, "%d UDP packets to %s into %s" % (sent, TUN_TARGET, TUN_DEV))

        check = "N1: a private name through DIRECT (ForceIPv4) from an inbound whose rule picks DIRECT"
        step(label + " " + check)
        before = tcp_private.snapshot()
        socks_get(CLIENT_PORTS["direct"], "private-n1.%s" % ZONE, 8080)
        after = tcp_private.snapshot()
        expect(check, after[0] > before[0], "%d handshakes" % (after[0] - before[0]))

        check = "N2: a private name from an ordinary user (IPIfNonMatch sends it to BLOCK on both builds)"
        step(label + " " + check)
        before = tcp_private.snapshot()
        socks_get(CLIENT_PORTS["users"], "private-n2.%s" % ZONE, 8080)
        after = tcp_private.snapshot()
        expect(check, after[0] > before[0], "%d handshakes" % (after[0] - before[0]), base_exposes=False)

        check = "C1: the CGNAT literal from an ordinary user through rule#2 and DIRECT"
        step(label + " " + check)
        took, first, body, err = socks_get(CLIENT_PORTS["users"], CGNAT, 8080)
        expect_served(check, body.endswith(b"ok"), "%.3fs %s" % (took, err or body[-12:]))

        check = "C2: a CGNAT name through DIRECT from an inbound whose rule picks DIRECT"
        step(label + " " + check)
        took, first, body, err = socks_get(CLIENT_PORTS["direct"], "cgnat-c2.%s" % ZONE, 8080)
        expect_served(check, body.endswith(b"ok"), "%.3fs %s" % (took, err or body[-12:]))

        check = "C3: a CGNAT name through the interface-bound lan_wireguard"
        step(label + " " + check)
        before = tcp_cgnat.snapshot()
        socks_get(CLIENT_PORTS["lan"], "cgnat-c3.%s" % ZONE, 8080)
        after = tcp_cgnat.snapshot()
        expect(check, after[0] > before[0], "%d handshakes (fleet: refused on the guard, reached before)" % (after[0] - before[0]))

        check = "C4: XUDP datagrams to the CGNAT sink from an ordinary user through rule#2 and DIRECT"
        step(label + " " + check)
        before = udp_cgnat.count()
        flow = SocksUDP(CLIENT_PORTS["users"])
        for _ in range(5):
            flow.send(CGNAT, 5353, b"cgnat")
            time.sleep(0.1)
        time.sleep(1.0)
        flow.close()
        got = udp_cgnat.count() - before
        expect_served(check, got > 0, "CGNAT sink %d datagrams" % got, finding=True)

        for inbound in ("users", "lan", "direct", "wg"):
            check = "P%s: a public name through the %s inbound" % (inbound[0], inbound)
            step(label + " " + check)
            took, first, body, err = socks_get(CLIENT_PORTS[inbound], "public-p%s.%s" % (inbound, ZONE), 8080)
            if inbound == "wg":
                results.record(label, check, "body %r" % body[-4:], True, "informational: the tun_wireguard device has no route to %s; %s" % (PUBLIC, err))
            else:
                expect_served(check, body.endswith(b"ok"), "%.3fs %s" % (took, err or body[-12:]))

        for inbound in ("direct", "lan"):
            check = "M-%s: a name with a public and a private A record, 10 requests" % inbound
            step(label + " " + check)
            before_pub, before_priv = tcp_public.snapshot(), tcp_private.snapshot()
            served = 0
            for i in range(10):
                took, first, body, err = socks_get(CLIENT_PORTS[inbound], "mixed-%s%d.%s" % (inbound, i, ZONE), 8080, 6)
                served += 1 if body.endswith(b"ok") else 0
            pub = tcp_public.snapshot()[0] - before_pub[0]
            priv = tcp_private.snapshot()[0] - before_priv[0]
            results.record(label, check, "served %d of 10" % served, True, "informational: public sink %d handshakes, private sink %d handshakes" % (pub, priv))

        for prefix, what in (("v4compat-", "an IPv4-compatible AAAA ::%s" % PRIVATE), ("nat64-", "a NAT64 AAAA 64:ff9b::%s" % PRIVATE)):
            check = "R-%s: a name whose only answer is %s, through lan_wireguard" % (prefix.rstrip("-"), what)
            step(label + " " + check)
            before = tcp_private.snapshot()
            took, first, body, err = socks_get(CLIENT_PORTS["lan"], "%sr.%s" % (prefix, ZONE), 8080, 6)
            after = tcp_private.snapshot()
            results.record(label, check, "%d handshakes at %s" % (after[0] - before[0], tcp_private.name), after[0] == before[0], "informational residual: %.2fs %s" % (took, err or body[-12:]))


def ttfb_phase(results, builds, out, dns):
    table = []
    dns.delay = 0.02
    for label, binary, guarded in builds:
        with XrayPair(label + "-perf", binary, out):
            for inbound in ("lan", "direct"):
                for i in range(3):
                    socks_get(CLIENT_PORTS[inbound], "perf-warm%d%s.%s" % (i, inbound, ZONE), 8080)
                times, before = [], len(dns.queries("perf-"))
                for i in range(30):
                    took, first, body, err = socks_get(CLIENT_PORTS[inbound], "perf-%s%s%d.%s" % (label, inbound, i, ZONE), 8080)
                    if first is not None and body.endswith(b"ok"):
                        times.append(first * 1000)
                queries = len(dns.queries("perf-")) - before
                row = {"build": label, "inbound": inbound, "ok": len(times), "median_ms": round(statistics.median(times), 2) if times else None,
                       "p90_ms": round(sorted(times)[int(len(times) * 0.9) - 1], 2) if times else None, "dns_queries_per_request": round(queries / 30.0, 2)}
                table.append(row)
                say("PERF", json.dumps(row))
    dns.delay = 0.0
    return table


def udp_rate_phase(results, builds, out, udp_cgnat, udp_public):
    table = []
    for label, binary, guarded in builds:
        with XrayPair(label + "-udp", binary, out) as pair:
            for target, sink in ((CGNAT, udp_cgnat), (PUBLIC, udp_public)):
                flow = SocksUDP(CLIENT_PORTS["users"])
                flow.send(target, 5353, b"warm")
                time.sleep(0.5)
                before_sink, before_cpu = sink.count(), cpu_seconds(pair.node_pid())
                payload = b"x" * 100
                started = time.perf_counter()
                for i in range(20000):
                    flow.send(target, 5353, payload)
                    if i % 200 == 199:
                        time.sleep(0.002)
                elapsed = time.perf_counter() - started
                time.sleep(1.5)
                delivered = sink.count() - before_sink
                cpu = cpu_seconds(pair.node_pid()) - before_cpu
                flow.close()
                row = {"build": label, "target": target, "sent": 20000, "delivered": delivered, "send_seconds": round(elapsed, 2),
                       "node_cpu_seconds": round(cpu, 3), "node_cpu_us_per_delivered": round(cpu * 1e6 / delivered, 2) if delivered else None}
                table.append(row)
                say("UDPRATE", json.dumps(row))
                results.record(label, "UDP rate: XUDP datagrams to %s from an ordinary user" % target, "delivered %d of 20000" % delivered,
                               delivered >= 18000, "node CPU %.3f s" % cpu, finding=True)
    return table


def first_nameserver():
    for path in (RESOLV, "/run/systemd/resolve/resolv.conf"):
        try:
            with open(path) as handle:
                for line in handle:
                    parts = line.split()
                    if len(parts) >= 2 and parts[0] == "nameserver" and parts[1] != LOOP:
                        return parts[1]
        except OSError:
            continue
    return None


def on_term(signum, frame):
    say("signal", signum, "- cleaning up")
    for proc in PROCS:
        if proc.poll() is None:
            proc.kill()
    while CLEANUPS:
        CLEANUPS.pop()()
    os._exit(4)


def prepare():
    for address in (PRIVATE + "/32", CGNAT + "/32", PUBLIC + "/32"):
        run(["ip", "addr", "add", address, "dev", LAN_DEV])
        CLEANUPS.append(lambda address=address: run(["ip", "addr", "del", address, "dev", LAN_DEV], check=False))
    run(["ip", "link", "add", TUN_DEV, "type", "dummy"])
    CLEANUPS.append(lambda: run(["ip", "link", "del", TUN_DEV], check=False))
    run(["ip", "link", "set", TUN_DEV, "up"])
    run(["ip", "addr", "add", TUN_NET, "dev", TUN_DEV])
    saved = ("link", os.readlink(RESOLV)) if os.path.islink(RESOLV) else ("file", open(RESOLV).read())

    def restore():
        if os.path.lexists(RESOLV):
            os.remove(RESOLV)
        if saved[0] == "link":
            os.symlink(saved[1], RESOLV)
        else:
            with open(RESOLV, "w") as handle:
                handle.write(saved[1])
        say("restored", RESOLV)

    CLEANUPS.append(restore)
    dns = ScriptedDNS(first_nameserver())
    say("forwarding names outside the test zone to", dns.upstream)
    os.remove(RESOLV)
    with open(RESOLV, "w") as handle:
        handle.write("nameserver 127.0.0.1\noptions timeout:1 attempts:1\n")
    return dns


def write_summary(results, ttfb, udp, path):
    lines = ["## Hostile round 2 harness", "", "| build | check | outcome | ok | detail |", "|---|---|---|---|---|"]
    for row in results.rows:
        lines.append("| %s | %s | %s | %s | %s |" % (row["build"], row["check"], row["outcome"], "yes" if row["ok"] else "NO", str(row["detail"]).replace("|", "/")))
    lines += ["", "| build | inbound | ok | median ms | p90 ms | DNS queries per request |", "|---|---|---|---|---|---|"]
    for row in ttfb:
        lines.append("| %(build)s | %(inbound)s | %(ok)s | %(median_ms)s | %(p90_ms)s | %(dns_queries_per_request)s |" % row)
    lines += ["", "| build | target | sent | delivered | node CPU s | CPU us per delivered |", "|---|---|---|---|---|---|"]
    for row in udp:
        lines.append("| %(build)s | %(target)s | %(sent)s | %(delivered)s | %(node_cpu_seconds)s | %(node_cpu_us_per_delivered)s |" % row)
    text = "\n".join(lines) + "\n"
    print(text, flush=True)
    if path:
        with open(path, "a") as handle:
            handle.write(text)


QUIC_INITIAL = b""


def main():
    global QUIC_INITIAL
    parser = argparse.ArgumentParser()
    parser.add_argument("--guard", required=True)
    parser.add_argument("--base", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--quic-hex", required=True)
    parser.add_argument("--fix")
    args = parser.parse_args()
    with open(args.quic_hex) as handle:
        QUIC_INITIAL = bytes.fromhex(handle.read().strip())
    os.makedirs(args.out, exist_ok=True)
    signal.signal(signal.SIGTERM, on_term)
    signal.signal(signal.SIGINT, on_term)
    threading.Thread(target=watchdog, args=(2400,), daemon=True).start()
    results = Results()
    try:
        dns = prepare()
        sinks = (TCPSink(PRIVATE, 8080), TCPSink(CGNAT, 8080), TCPSink(PUBLIC, 8080),
                 UDPSink(PRIVATE, 5353), UDPSink(CGNAT, 5353), UDPSink(PUBLIC, 5353), UDPSink(PRIVATE, 443))
        counters = (TunCounter("tcp"), TunCounter("udp"))
        builds = [("base", args.base, False), ("guard", args.guard, True)]
        if args.fix:
            builds.append(("fix", args.fix, True))
        for label, binary, guarded in builds:
            security_phase(results, label, binary, guarded, args.out, dns, sinks, counters)
        step("time to first byte")
        ttfb = ttfb_phase(results, builds, args.out, dns)
        step("UDP rate")
        udp = udp_rate_phase(results, builds, args.out, sinks[4], sinks[5])
        write_summary(results, ttfb, udp, os.environ.get("GITHUB_STEP_SUMMARY"))
    finally:
        while CLEANUPS:
            CLEANUPS.pop()()
    say("findings (RED):", len(results.findings))
    for line in results.findings:
        say("  RED", line)
    say("failures:", len(results.failures))
    for line in results.failures:
        say("  FAIL", line)
    sys.exit(1 if results.failures else 0)


if __name__ == "__main__":
    main()
