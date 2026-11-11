#!/bin/sh
# Tests packaging/bin/set-port with a fake daemon and a fake rc.d script.
# Fake rules: port 1234 is "occupied" (-check fails), the server does not
# come up on port 4321 (restart fails).
set -u
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/base/bin" "$T/cfg"
cat > "$T/base/bin/ccu-modbusserverd" <<'EOF'
#!/bin/sh
conf=$2
port=$(sed -nE 's/.*"port"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' "$conf" | head -1)
case "$3" in
  -print-port) [ -n "$port" ] && echo "$port" ;;
  -check) [ "$port" != 1234 ] ;;
esac
EOF
cat > "$T/rcd" <<EOF
#!/bin/sh
port=\$(sed -nE 's/.*"port"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' "$T/cfg/config.json")
echo "\$1 \$port" >> "$T/rcd.log"
[ "\$port" != 4321 ]
EOF
chmod 755 "$T/base/bin/ccu-modbusserverd" "$T/rcd"
export CCU_MS_BASE="$T/base" CCU_MS_CFGDIR="$T/cfg" CCU_MS_RCD="$T/rcd" CCU_MS_LOG="$T/log"
S=$(dirname "$0")/../packaging/bin/set-port
fails=0
reset() { printf '{\n  "schema": 1,\n  "port": 502,\n  "write_allow": ["10.0.0.0/8"]\n}\n' > "$T/cfg/config.json"; : > "$T/rcd.log"; }
port() { sed -nE 's/.*"port"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' "$T/cfg/config.json"; }
check() { # name expected-exit expected-port-in-file output-substring
  if [ "$rc" -eq "$2" ] && [ "$(port)" = "$3" ] && echo "$out" | grep -q -- "$4"; then
    echo "OK   $1"
  else
    echo "FAIL $1: rc=$rc port=$(port) out=$out"; fails=$((fails+1))
  fi
}
reset; out=$(sh "$S" 1502); rc=$?; check "Wechsel 502->1502" 0 1502 '"ok":true'
grep -q '10.0.0.0/8' "$T/cfg/config.json" && echo "OK   übrige Felder erhalten" || { echo "FAIL übrige Felder"; fails=$((fails+1)); }
[ "$(cat "$T/rcd.log")" = "restart 1502" ] && echo "OK   rc.d restart aufgerufen" || { echo "FAIL rc.d: $(cat "$T/rcd.log")"; fails=$((fails+1)); }
reset; out=$(sh "$S" 502); rc=$?; check "gleicher Port: nichts tun" 0 502 '"ok":true'
[ ! -s "$T/rcd.log" ] && echo "OK   kein Neustart bei gleichem Port" || { echo "FAIL Neustart"; fails=$((fails+1)); }
reset; out=$(sh "$S" abc); rc=$?; check "keine Zahl" 1 502 'Zahl'
reset; out=$(sh "$S" 0); rc=$?; check "Port 0" 1 502 '65535'
reset; out=$(sh "$S" 70000); rc=$?; check "Port 70000" 1 502 '65535'
reset; out=$(sh "$S" "502;reboot"); rc=$?; check "Einschleusung" 1 502 'Zahl'
reset; out=$(sh "$S" 1234); rc=$?; check "belegter Port" 1 502 'belegt'
[ ! -s "$T/rcd.log" ] && echo "OK   kein Neustart bei belegtem Port" || { echo "FAIL Neustart"; fails=$((fails+1)); }
reset; out=$(sh "$S" 4321); rc=$?; check "Startfehler -> zurück auf 502" 1 502 'Port 502 bleibt aktiv'
[ "$(cat "$T/rcd.log" | tr '\n' ' ')" = "restart 4321 start 502 " ] && echo "OK   Rücknahme gestartet" || { echo "FAIL Rücknahme: $(cat "$T/rcd.log")"; fails=$((fails+1)); }
[ ! -e "$T/cfg/config.json.prev" ] && [ ! -e "$T/cfg/config.json.check" ] && echo "OK   keine Reste" || { echo "FAIL Reste"; ls "$T/cfg"; fails=$((fails+1)); }
mkdir /tmp/ccu-modbusserver-setport.lock 2>/dev/null && { reset; out=$(sh "$S" 1502); rc=$?; rmdir /tmp/ccu-modbusserver-setport.lock; check "Sperre" 1 502 'läuft bereits'; }
[ $fails -eq 0 ] && echo "PASS" || echo "FAIL ($fails)"
exit $fails
