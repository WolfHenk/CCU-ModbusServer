#!/bin/sh
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/test_firewall.sh
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.1.1
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
#
# Tests packaging/bin/firewall on a CCU against COPIES of
# /lib/libfirewall.tcl and /etc/config/firewall.conf in a temp directory.
# The live firewall is neither changed nor applied.
#
# Usage on the CCU: sh test_firewall.sh /path/to/firewall

FW=${1:?path to firewall script}
D=/usr/local/tmp/cms-fwtest.$$
FAILS=0
export CCU_MODBUSSERVER_TEST_DIR=$D

ok()   { echo "OK   $*"; }
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
ports() { sed -n 's/^USERPORTS *= *//p' "$D/firewall.conf"; }
owner() { cat "$D/firewall.owner" 2>/dev/null; }
reset() {
  rm -rf "$D"; mkdir -p "$D"
  cp /lib/libfirewall.tcl "$D/"
  grep -v '^USERPORTS' /etc/config/firewall.conf > "$D/firewall.conf"
  if [ -n "$1" ]; then
    sed -i "s/^MODE/USERPORTS = $1\n\nMODE/" "$D/firewall.conf"
  fi
}
check() { # name expected-ports expected-owner
  p=$(ports); o=$(owner)
  if [ "$p" = "$2" ] && [ "$o" = "$3" ]; then ok "$1"; else fail "$1: ports='$p' (soll '$2') owner='$o' (soll '$3')"; fi
}
FWT="tclsh $FW"

reset;              $FWT open 502;  check "open 502" "502" "502"
                    $FWT open 502;  check "open 502 idempotent" "502" "502"
                    $FWT open 1502; check "Portwechsel 502->1502" "1502" "1502"
                    $FWT close;     check "close" "" ""
reset "8080 502";   $FWT open 502;  check "fremder Port 502 bleibt fremd" "8080 502" ""
                    $FWT close;     check "close laesst fremden 502" "8080 502" ""
reset "1502";       $FWT open 502;  check "eigener 502 neben fremdem 1502" "1502 502" "502"
                    $FWT open 1502; check "Wechsel auf fremden 1502: eigener 502 weg, 1502 bleibt fremd" "1502" ""
                    $FWT close;     check "close: fremder 1502 bleibt" "1502" ""
reset;              $FWT open 502
CCU_MODBUSSERVER_TEST_FAIL_SAVE=1 $FWT open 1502; rc=$?
[ $rc -eq 1 ] && ok "Speicherfehler -> rc 1" || fail "Speicherfehler rc=$rc"
                    check "Speicherfehler: Besitz 502 bleibt erhalten" "502" "502"
CCU_MODBUSSERVER_TEST_FAIL_SAVE=1 $FWT close; rc=$?
[ $rc -eq 1 ] && ok "Speicherfehler bei close -> rc 1" || fail "close rc=$rc"
                    check "Speicherfehler bei close: Besitz bleibt" "502" "502"
$FWT open 0 2>/dev/null;     [ $? -eq 1 ] && ok "Port 0 abgelehnt" || fail "Port 0"
$FWT open 65536 2>/dev/null; [ $? -eq 1 ] && ok "Port 65536 abgelehnt" || fail "Port 65536"
$FWT bogus 2>/dev/null;      [ $? -eq 1 ] && ok "falscher Befehl abgelehnt" || fail "falscher Befehl"
n=$(grep -c '^\[SERVICE' "$D/firewall.conf"); m=$(grep -c '^\[SERVICE' /etc/config/firewall.conf)
[ "$n" = "$m" ] && ok "Dienste erhalten ($n)" || fail "Dienste $n statt $m"
rm "$D/libfirewall.tcl"
$FWT open 502 2>/dev/null; [ $? -eq 2 ] && ok "fehlende Bibliothek -> rc 2" || fail "fehlende Bibliothek"
rm -rf "$D"
echo "RESULT: $([ $FAILS -eq 0 ] && echo PASS || echo "$FAILS FAILED")"
exit $FAILS
