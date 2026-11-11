#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/e2e_pymodbus.py
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
# Independent client test with pymodbus (not our own frame code): reads
# every active row of the client sheet and the status block, checks
# values against the sheet, valid bits and exception codes. Read-only;
# no writes. Usage: e2e_pymodbus.py HOST sheet.json [PORT]
import json
import struct
import sys

from pymodbus.client import ModbusTcpClient
from pymodbus.pdu import ExceptionResponse

host, sheet = sys.argv[1], json.load(open(sys.argv[2]))
port = int(sys.argv[3]) if len(sys.argv) > 3 else 502
fails = 0


def check(ok, text):
    global fails
    print(("OK   " if ok else "FAIL ") + text)
    if not ok:
        fails += 1


c = ModbusTcpClient(host, port=port, timeout=5)
check(c.connect(), f"Verbindung {host}:{port}")

st = c.read_input_registers(0, count=4, device_id=1)
check(not st.isError(), f"Statusblock IR 0-3 {getattr(st, 'registers', st)}")
if not st.isError():
    check(st.registers[0] & 1 == 1, "Bit 0 = CCU-Logik erreichbar")
    n = len([r for r in sheet["rows"] if not r["reserved"]])
    check(st.registers[3] == n, f"IR 3 Anzahl Freigaben {st.registers[3]} = {n}")

wo = sheet.get("word_order", "ABCD")
for r in sheet["rows"]:
    if r["reserved"] or r["orphan"]:
        continue
    t, a, n, f = r["table"], r["address"], r["count"], r["format"]
    if t == "Coil":
        resp = c.read_coils(a, count=1, device_id=1)
        got = None if resp.isError() else int(resp.bits[0])
    elif t == "Discrete Input":
        resp = c.read_discrete_inputs(a, count=1, device_id=1)
        got = None if resp.isError() else int(resp.bits[0])
    elif t == "Input Register":
        resp = c.read_input_registers(a, count=n, device_id=1)
        got = None if resp.isError() else resp.registers
    else:
        resp = c.read_holding_registers(a, count=n, device_id=1)
        got = None if resp.isError() else resp.registers
    if got is None:
        check(False, f"{r['label']}: Lesefehler {resp}")
        continue
    if f == "bit":
        want = 1 if r["value"] in ("true", "1") else 0
    elif f == "float32":
        regs = got if wo == "ABCD" else [got[1], got[0]]
        val = struct.unpack(">f", struct.pack(">HH", *regs))[0]
        want = round(float(r["value"]), 3)
        got = round(val, 3)
    else:
        regs = got if wo == "ABCD" else got[::-1]
        raw = 0
        for w in regs:
            raw = raw << 16 | w
        if f.startswith("int") and raw >= 1 << (16 * n - 1):
            raw -= 1 << (16 * n)
        got = raw
        want = round(float(r["value"]) * (r["scale"] or 1))
    check(got == want, f"{r['label']} ({t} {a}) pymodbus={got} sheet={want}")
    vb = c.read_discrete_inputs(r["valid_bit"], count=1, device_id=1)
    check(not vb.isError() and vb.bits[0] == r["valid"], f"  Gültig-Bit DI {r['valid_bit']} = {int(r['valid'])}")

# protocol checks with a foreign client
e = c.read_holding_registers(60000, count=1, device_id=1)
check(isinstance(e, ExceptionResponse) and e.exception_code == 2, f"unbelegte Adresse -> Exception 02 ({e})")
# (126 registers: pymodbus refuses to send that; covered by e2e_status.py)
for uid in (0, 7, 255):
    s = c.read_input_registers(0, count=1, device_id=uid)
    check(not s.isError(), f"Unit-ID {uid} beantwortet")
c.close()
print("RESULT:", "PASS" if not fails else f"FAIL ({fails})")
sys.exit(1 if fails else 0)
