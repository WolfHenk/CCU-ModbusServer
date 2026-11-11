#!/bin/tclsh
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        packaging/www/index.cgi
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
#
# Stage 1: status page only. Device selection follows in stage 3.

source /usr/local/addons/ccu-modbusserver/lib/session.tcl
set base /usr/local/addons/ccu-modbusserver
set cfgdir /usr/local/etc/config/addons/ccu-modbusserver

proc html {s} { string map {& &amp; < &lt; > &gt; \" &quot;} $s }

proc readfile {p} {
    if {[catch {set f [open $p r]}]} { return "" }
    set s [read $f]
    close $f
    return [string trim $s]
}

set sid [query_value sid]
puts "Content-Type: text/html; charset=utf-8\r"
puts "Cache-Control: no-store\r\n"
if {![check_session $sid]} {
    puts {<!doctype html><html><meta charset="utf-8"><p>Sitzung ungueltig. Bitte erneut ueber die OpenCCU anmelden.</p></html>}
    exit 0
}

set version [readfile $base/VERSION]
set port 502
regexp {"port"\s*:\s*([0-9]+)} [readfile $cfgdir/config.json] all port
set running 0
set pid [readfile /var/run/ccu-modbusserver.pid]
if {$pid ne "" && [file exists /proc/$pid]} { set running 1 }
set owned [readfile $cfgdir/firewall.owner]
set fwconf [readfile /etc/config/firewall.conf]
set fwopen 0
if {[regexp -line {^USERPORTS\s*=\s*(.*)$} $fwconf all ports] && [lsearch -exact $ports $port] >= 0} {
    set fwopen 1
}

puts {<!doctype html>}
puts {<html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">}
puts "<title>CCU-ModbusServer</title><link rel=\"stylesheet\" href=\"style.css?v=[html $version]\"></head><body>"
puts "<header class=\"top\"><img src=\"img/modbus-logo.png\" class=\"logo\" alt=\"Modbus\"><div><h1>CCU-ModbusServer</h1><div class=\"sub\">Modbus TCP Server für CCU/OpenCCU · Version [html $version]</div></div></header>"
puts {<main>}
puts {<section class="box empty"><strong>0 Werte freigegeben</strong> – es werden keine Daten bereitgestellt.<br><span class="muted">Die Auswahl von Geräten und Werten folgt in einer späteren Version.</span></section>}
puts {<section class="box"><table>}
if {$running} {
    puts "<tr><th>Server</th><td class=\"ok\">läuft</td></tr>"
} else {
    puts "<tr><th>Server</th><td class=\"bad\">gestoppt – siehe /var/log/ccu-modbusserver.log</td></tr>"
}
puts "<tr><th>Port</th><td>[html $port]</td></tr>"
if {$fwopen} {
    if {$owned eq $port} {
        puts "<tr><th>Firewall</th><td class=\"ok\">Port [html $port] freigegeben (durch CCU-ModbusServer)</td></tr>"
    } else {
        puts "<tr><th>Firewall</th><td class=\"ok\">Port [html $port] freigegeben (bereits vorher vorhanden)</td></tr>"
    }
} else {
    puts "<tr><th>Firewall</th><td class=\"bad\">Port [html $port] nicht als Benutzerport eingetragen. Bitte unter Einstellungen &gt; Systemsteuerung &gt; Firewall freigeben.</td></tr>"
}
puts {</table></section>}
puts {<footer class="copyright">© 2026 Wolfram Henkel · CCU-ModbusServer-Lizenz 1.1</footer>}
puts {</main></body></html>}
