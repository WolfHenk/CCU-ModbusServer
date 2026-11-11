#!/bin/sh
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        tools/test_rcd_ccu.sh
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
# Start-path tests of the installed add-on on the TEST CCU ONLY.
# Changes the live firewall and the add-on configuration temporarily and
# restores port 502 at the end. Never run on a productive CCU.

R=/usr/local/etc/config/rc.d/ccu-modbusserver
BASE=/usr/local/addons/ccu-modbusserver
CONF=/usr/local/etc/config/addons/ccu-modbusserver/config.json
FAILS=0
ok()   { echo "OK   $*"; }
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
ports() { sed -n 's/^USERPORTS *= *//p' /etc/config/firewall.conf; }
owner() { "$BASE/bin/firewall" status; }
setport() { sed -i "s/\"port\": *[0-9]*/\"port\": $1/" "$CONF"; }
expect() { # name ports owner running(0/1)
  p=$(ports); o=$(owner); r=0; [ -f /var/run/ccu-modbusserver.pid ] && kill -0 "$(cat /var/run/ccu-modbusserver.pid)" 2>/dev/null && r=1
  if [ "$p" = "$2" ] && [ "$o" = "$3" ] && [ "$r" = "$4" ]; then ok "$1"; else fail "$1: ports='$p'/'$2' owner='$o'/'$3' running=$r/$4"; fi
}

# 0. starting point: running on 502, owned
$R start
expect "Ausgangslage" "502" "502" 1

# 1. port busy: nothing in the firewall may change
$R stop
setport 1503
"$BASE/bin/ccu-modbusserverd" -config "$CONF" -listen :1503 >/dev/null 2>&1 &
BLOCK=$!
sleep 1
$R start; rc=$?
[ $rc -ne 0 ] && ok "belegter Port -> Start rc $rc" || fail "belegter Port: rc 0"
expect "belegter Port: Firewall unveraendert, kein Dienst" "502" "502" 0
kill $BLOCK; sleep 1

# 2. server dies right after start: firewall change of THIS start is undone
cp "$BASE/bin/ccu-modbusserverd" /usr/local/tmp/cms-real
cat > "$BASE/bin/ccu-modbusserverd" <<'EOF'
#!/bin/sh
for a in "$@"; do case "$a" in -print-port|-check|-probe) exec /usr/local/tmp/cms-real "$@";; esac; done
exit 3
EOF
chmod 755 "$BASE/bin/ccu-modbusserverd"
setport 1504
$R start; rc=$?
[ $rc -ne 0 ] && ok "Abbruch nach Start -> rc $rc" || fail "Abbruch: rc 0"
expect "Abbruch: Freigabe 1504 zurueckgenommen, keine PID-Datei" "" "" 0
[ -f /var/run/ccu-modbusserver.pid ] && fail "PID-Datei liegt noch" || ok "keine PID-Datei"
cp /usr/local/tmp/cms-real "$BASE/bin/ccu-modbusserverd"; rm -f /usr/local/tmp/cms-real

# 3. invalid configuration: nothing changes
setport 70000
$R start; rc=$?
[ $rc -ne 0 ] && ok "ungueltiger Port in config -> rc $rc" || fail "ungueltige config: rc 0"
expect "ungueltige config: Firewall unveraendert" "" "" 0

# 4. back to 502
setport 502
$R start
expect "wieder 502" "502" "502" 1
grep 'rc.d:' /var/log/ccu-modbusserver.log | tail -6
echo "RESULT: $([ $FAILS -eq 0 ] && echo PASS || echo "$FAILS FAILED")"
exit $FAILS
