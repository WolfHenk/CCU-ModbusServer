#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/e2e_status.py
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.1.0
#
# Copyright (c) 2026 Wolfram Henkel.
#
# Lizenz:       CCU-ModbusServer-Lizenz für kostenfreie Nutzung und
#               Weitergabe, Version 1.1.
#
# Private und gewerbliche Nutzung sind erlaubt.
# Die Software und Änderungen dürfen nur kostenlos unter
# derselben Lizenz und mit vollständigem Quellcode weitergegeben werden.
#
# Vollständiger Lizenztext: LICENSE
# -----------------------------------------------------------------------------
"""End-to-end check of an installed stage-1 server (no exports).

Usage: e2e_status.py HOST [PORT]
Reads the status block and checks that every other table answers 02.
"""
import socket
import struct
import sys
import time


def call(sock, tid, pdu, unit=1):
    sock.sendall(struct.pack(">HHHB", tid, 0, len(pdu) + 1, unit) + pdu)
    hdr = b""
    while len(hdr) < 7:
        chunk = sock.recv(7 - len(hdr))
        if not chunk:
            raise RuntimeError("connection closed")
        hdr += chunk
    rtid, pid, length, runit = struct.unpack(">HHHB", hdr)
    body = b""
    while len(body) < length - 1:
        chunk = sock.recv(length - 1 - len(body))
        if not chunk:
            raise RuntimeError("connection closed")
        body += chunk
    assert rtid == tid and pid == 0 and runit == unit, (rtid, pid, runit)
    return body


def main():
    host = sys.argv[1]
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 502
    fails = 0

    def check(name, cond, detail=""):
        nonlocal fails
        print(("OK   " if cond else "FAIL ") + name + (" " + detail if detail else ""))
        if not cond:
            fails += 1

    s = socket.create_connection((host, port), timeout=3)
    r = call(s, 1, struct.pack(">BHH", 4, 0, 4))
    check("FC04 status block 0..3", r[:2] == b"\x04\x08", r.hex())
    flags, up_hi, up_lo, exports = struct.unpack(">HHHH", r[2:])
    up1 = (up_hi << 16) | up_lo
    check("exports == 0", exports == 0, str(exports))
    check("CCU-Verbindung (Bit 0) ok", flags & 1 == 1, str(flags))
    time.sleep(2.2)
    r = call(s, 2, struct.pack(">BHH", 4, 1, 2))
    up2 = struct.unpack(">I", r[2:6])[0]
    check("uptime counter runs", up2 >= up1 + 2, f"{up1} -> {up2}")
    check("FC04 second half -> 02", call(s, 3, struct.pack(">BHH", 4, 2, 1)) == b"\x84\x02")
    check("FC04 beyond block -> 02", call(s, 4, struct.pack(">BHH", 4, 0, 5)) == b"\x84\x02")
    check("FC01 empty -> 02", call(s, 5, struct.pack(">BHH", 1, 0, 1)) == b"\x81\x02")
    check("FC02 empty -> 02", call(s, 6, struct.pack(">BHH", 2, 0, 1)) == b"\x82\x02")
    check("FC03 empty -> 02", call(s, 7, struct.pack(">BHH", 3, 0, 1)) == b"\x83\x02")
    check("FC05 empty -> 02", call(s, 8, struct.pack(">BHH", 5, 0, 0xFF00)) == b"\x85\x02")
    check("FC06 empty -> 02", call(s, 9, struct.pack(">BHH", 6, 0, 1)) == b"\x86\x02")
    check("FC08 -> 01", call(s, 10, b"\x08\x00\x00\x00\x00") == b"\x88\x01")
    check("FC04 qty 126 -> 03", call(s, 11, struct.pack(">BHH", 4, 0, 126)) == b"\x84\x03")
    for unit in (0, 17, 255):
        check(f"unit {unit} answered", call(s, 12, struct.pack(">BHH", 4, 3, 1), unit)[:2] == b"\x04\x02")
    s.close()
    print("RESULT:", "PASS" if fails == 0 else f"{fails} FAILED")
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
