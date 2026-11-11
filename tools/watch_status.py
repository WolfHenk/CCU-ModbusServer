#!/usr/bin/env python3
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/watch_status.py
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.2.0
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
"""Prints status register 0 (bit 0 = CCU connection) once per second.

Usage: watch_status.py HOST SECONDS [PORT]
Output: one line per second "t=<s> flags=<n>" and a summary of changes.
"""
import socket
import struct
import sys
import time


def read_flags(host, port):
    with socket.create_connection((host, port), timeout=2) as s:
        s.sendall(struct.pack(">HHHBBHH", 1, 0, 6, 1, 4, 0, 1))
        r = s.recv(64)
        return struct.unpack(">H", r[9:11])[0]


def main():
    host, secs = sys.argv[1], int(sys.argv[2])
    port = int(sys.argv[3]) if len(sys.argv) > 3 else 502
    last = None
    start = time.time()
    while time.time() - start < secs:
        t = int(time.time() - start)
        try:
            f = read_flags(host, port)
        except OSError as e:
            f = f"ERR {e}"
        if f != last:
            print(f"t={t}s flags={f}", flush=True)
            last = f
        time.sleep(1)


if __name__ == "__main__":
    main()
