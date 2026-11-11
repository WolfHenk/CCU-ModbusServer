#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/e2e_stage3.py
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.3.0
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
"""End-to-end test of exports on the TEST CCU (never on a productive CCU).

Usage: e2e_stage3.py HOST SHEET_JSON
SHEET_JSON is the output of GET /api/sheet. Every row is read over
Modbus and compared with the CCU value from the sheet; valid bits are
checked; one writable coil (a system variable) is toggled and restored.
"""
import json
import socket
import struct
import sys
import time

tid = 0


def call(s, pdu, unit=1):
    global tid
    tid = (tid + 1) & 0xFFFF
    s.sendall(struct.pack(">HHHB", tid, 0, len(pdu) + 1, unit) + pdu)
    h = b""
    while len(h) < 7:
        h += s.recv(7 - len(h))
    rtid, pid, ln, u = struct.unpack(">HHHB", h)
    b = b""
    while len(b) < ln - 1:
        b += s.recv(ln - 1 - len(b))
    assert rtid == tid and pid == 0
    return b


def read_bits(s, fc, addr, n):
    r = call(s, struct.pack(">BHH", fc, addr, n))
    if r[0] & 0x80:
        raise RuntimeError(f"exception {r[1]:02x}")
    return [(r[2 + i // 8] >> (i % 8)) & 1 for i in range(n)]


def read_regs(s, fc, addr, n):
    r = call(s, struct.pack(">BHH", fc, addr, n))
    if r[0] & 0x80:
        raise RuntimeError(f"exception {r[1]:02x}")
    return list(struct.unpack(">" + "H" * n, r[2:]))


def main():
    host, sheet = sys.argv[1], json.load(open(sys.argv[2]))
    order = sheet["word_order"]
    fails = 0

    def check(name, cond, detail=""):
        nonlocal fails
        print(("OK   " if cond else "FAIL ") + name + (" " + detail if detail else ""))
        if not cond:
            fails += 1

    s = socket.create_connection((host, 502), timeout=3)
    st = read_regs(s, 4, 0, 4)
    check("Status: CCU-Logik erreichbar, Freigaben", st[0] & 1 == 1 and st[3] == len(sheet["rows"]), str(st))
    coil = None
    for r in sheet["rows"]:
        name = f'{r["label"]} ({r["table"]} {r["address"]})'
        if r["table"] in ("Coil", "Discrete Input"):
            fc = 1 if r["table"] == "Coil" else 2
            v = read_bits(s, fc, r["address"], 1)[0]
            want = 1 if r["value"] == "true" else 0
            check(name, v == want, f'modbus={v} ccu={r["value"]}')
            if r["table"] == "Coil" and r["access"] == "R/W":
                coil = r
        else:
            fc = 4 if r["table"] == "Input Register" else 3
            w = read_regs(s, fc, r["address"], r["count"])
            if r["format"] == "float32":
                hi, lo = (w[0], w[1]) if order == "ABCD" else (w[1], w[0])
                v = struct.unpack(">f", struct.pack(">HH", hi, lo))[0]
                check(name, abs(v - float(r["value"])) < 1e-3, f'modbus={v} ccu={r["value"]}')
            else:
                check(name, w[0] == int(float(r["value"]) * r["scale"]), f'modbus={w} ccu={r["value"]}')
        vb = read_bits(s, 2, r["valid_bit"], 1)[0]
        wb = read_bits(s, 2, r["warn_bit"], 1)[0]
        check(f'  Gültig-Bit DI {r["valid_bit"]}', vb == (1 if r["valid"] else 0), f'bit={vb} api={r["valid"]} ({r["reason"]})')
        check(f'  Warnbit DI {r["warn_bit"]}', wb == 0, f"bit={wb}")
    if coil:
        a = coil["address"]
        old = read_bits(s, 1, a, 1)[0]
        new = 1 - old
        r = call(s, struct.pack(">BHH", 5, a, 0xFF00 if new else 0))
        check(f"FC05 {coil['label']} -> {new}", r[0] == 5, r.hex())
        r = call(s, struct.pack(">BHH", 5, a, 0xFF00 if old else 0))
        check("sofort zurück -> 06 (Mindestabstand)", r == b"\x85\x06", r.hex())
        time.sleep(2.5)  # next poll cycle
        check("Rücklesen über Modbus", read_bits(s, 1, a, 1)[0] == new)
        r = call(s, struct.pack(">BHH", 5, a, 0xFF00 if new else 0))
        check("gleicher Wert: Quittung", r[0] == 5, r.hex())
        r = call(s, struct.pack(">BHH", 5, a, 0xFF00 if old else 0))
        check(f"zurücksetzen auf {old}", r[0] == 5, r.hex())
        time.sleep(2.5)
        check("Rücklesen nach Zurücksetzen", read_bits(s, 1, a, 1)[0] == old)
    # writes to read-only exports are rejected by the table (02)
    for r in sheet["rows"]:
        if r["table"] == "Input Register":
            x = call(s, struct.pack(">BHH", 6, r["address"], 1))
            check("FC06 auf Input Register -> 02", x == b"\x86\x02", x.hex())
            break
    s.close()
    print("RESULT:", "PASS" if fails == 0 else f"{fails} FAILED")
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
