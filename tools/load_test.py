#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/load_test.py
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.4.2
#
# Copyright (c) 2026 Wolfram Henkel.
#
# Lizenz:       CCU-ModbusServer-Lizenz für kostenfreie Nutzung und
#               Weitergabe, Version 1.1.
#
# Vollständiger Lizenztext: LICENSE
# -----------------------------------------------------------------------------
#
# Read-only load test: CONN parallel connections read the status block and
# the valid bits as fast as possible for MINUTES; reports requests,
# errors and latency percentiles per minute. No writes.
# Usage: load_test.py HOST MINUTES [CONN] [PORT]
import socket
import struct
import sys
import threading
import time

host, minutes = sys.argv[1], float(sys.argv[2])
conns = int(sys.argv[3]) if len(sys.argv) > 3 else 12
port = int(sys.argv[4]) if len(sys.argv) > 4 else 502
end = time.time() + minutes * 60
lock = threading.Lock()
stats = {"req": 0, "err": 0, "lat": [], "reconnect": 0}


def recvn(s, n):
    b = b""
    while len(b) < n:
        c = s.recv(n - len(b))
        if not c:
            raise ConnectionError("closed")
        b += c
    return b


def worker(k):
    tid = 0
    s = None
    while time.time() < end:
        try:
            if s is None:
                s = socket.create_connection((host, port), timeout=5)
            tid = (tid + 1) & 0xFFFF
            if tid % 2:
                pdu = struct.pack(">BHH", 4, 0, 4)        # IR 0..3
            else:
                pdu = struct.pack(">BHH", 2, 10001, 4)    # valid bits 1..4
            t0 = time.perf_counter()
            s.sendall(struct.pack(">HHHB", tid, 0, len(pdu) + 1, 1) + pdu)
            h = recvn(s, 7)
            rtid, _, ln, _ = struct.unpack(">HHHB", h)
            body = recvn(s, ln - 1)
            dt = time.perf_counter() - t0
            ok = rtid == tid and body[0] == pdu[0]
            with lock:
                stats["req"] += 1
                stats["lat"].append(dt)
                if not ok:
                    stats["err"] += 1
        except Exception:
            with lock:
                stats["err"] += 1
                stats["reconnect"] += 1
            if s:
                s.close()
            s = None
            time.sleep(0.5)
    if s:
        s.close()


threads = [threading.Thread(target=worker, args=(k,), daemon=True) for k in range(conns)]
for t in threads:
    t.start()
total = {"req": 0, "err": 0}
while time.time() < end:
    time.sleep(min(60, max(0.1, end - time.time())))
    with lock:
        lat = sorted(stats["lat"])
        req, err, rc = stats["req"], stats["err"], stats["reconnect"]
        stats.update(req=0, err=0, lat=[], reconnect=0)
    total["req"] += req
    total["err"] += err
    p = lambda q: lat[int(q * (len(lat) - 1))] * 1000 if lat else 0
    print(f"{time.strftime('%H:%M:%S')} req={req} err={err} reconnect={rc} p50={p(.5):.1f}ms p99={p(.99):.1f}ms max={p(1):.1f}ms", flush=True)
for t in threads:
    t.join(10)
print(f"TOTAL req={total['req']} err={total['err']}")
sys.exit(1 if total["err"] else 0)
