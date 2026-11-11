#!/bin/tclsh
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        packaging/www/index.cgi
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.4.0
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
# Add-on page: selection of devices and system variables, client sheet
# with print view and CSV, write permission list. Data comes from
# api.cgi (session-checked forwarding to the daemon's local API).

source /usr/local/addons/ccu-modbusserver/lib/session.tcl
set base /usr/local/addons/ccu-modbusserver
set cfgdir /usr/local/etc/config/addons/ccu-modbusserver

proc html {s} { string map {& &amp; < &lt; > &gt; \" &quot; ' &#39;} $s }

proc readfile {p} {
    if {[catch {set f [open $p r]}]} { return "" }
    set s [read $f]
    close $f
    return [string trim $s]
}

set sid [query_value sid]
puts "Content-Type: text/html; charset=utf-8\r"
puts "Cache-Control: no-store\r"
puts "X-Frame-Options: SAMEORIGIN\r\n"
if {![check_session $sid]} {
    puts {<!doctype html><html><meta charset="utf-8"><p>Sitzung ungültig. Bitte erneut über die OpenCCU anmelden.</p></html>}
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
set fwmode ""
regexp -line {^MODE\s*=\s*(\S+)} $fwconf all fwmode
if {$fwopen} {
    set fwtext "Port [html $port] ist in der OpenCCU-Firewall als Benutzerport eingetragen"
    if {$owned eq $port} { append fwtext " (durch CCU-ModbusServer)." } else { append fwtext " (war bereits vorhanden)." }
    append fwtext " Benutzerports sind nur aus dem eigenen Netz erreichbar, nicht aus dem Internet."
    set fwclass ok
} else {
    set fwtext "Port [html $port] ist nicht als Benutzerport eingetragen. Bitte unter Einstellungen &gt; Systemsteuerung &gt; Firewall freigeben."
    set fwclass bad
}
set v [html $version]

puts {<!doctype html>}
puts {<html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">}
puts "<title>CCU-ModbusServer</title><link rel=\"stylesheet\" href=\"style.css?v=$v\"></head>"
puts "<body data-sid=\"[html $sid]\" data-port=\"[html $port]\" data-version=\"$v\">"
puts "<header class=\"top noprint\"><img src=\"img/modbus-logo.png\" class=\"logo\" alt=\"Modbus\"><div><h1>CCU-ModbusServer</h1><div class=\"sub\">Modbus TCP Server für CCU/OpenCCU</div></div><div class=\"version\">Version <strong>$v</strong></div></header>"
puts {<main>}
puts {<div id="notice" class="notice hidden noprint" role="status"></div>}
puts {<section class="statusbar noprint">}
if {$running} {
    puts "<span class=\"lamp green\"></span><span id=\"srvState\">Server läuft</span>"
} else {
    puts "<span class=\"lamp red\"></span><span id=\"srvState\">Server gestoppt – siehe /var/log/ccu-modbusserver.log</span>"
}
puts "<span class=\"sep\">·</span><span>Port <strong>[html $port]</strong></span><span class=\"sep\">·</span><span id=\"exportCount\">…</span><span class=\"sep\">·</span><span id=\"ccuState\">…</span>"
puts {</section>}
puts "<p class=\"fw $fwclass noprint\">$fwtext</p>"
puts {<nav class="tabs noprint" role="tablist">}
puts {<button role="tab" data-tab="devices" class="active">Geräte</button><button role="tab" data-tab="sysvars">Systemvariablen</button><button role="tab" data-tab="exports">Freigaben</button><button role="tab" data-tab="sheet">Client-Blatt</button><button role="tab" data-tab="settings">Einstellungen</button>}
puts {</nav>}

puts {<section id="tab-devices" class="tab">}
puts {<div class="filters"><label>Name<input id="fName" type="search" placeholder="Gerät, Kanal oder Adresse"></label><label>Raum<select id="fRoom"><option value="">alle</option></select></label><label>Gewerk<select id="fFunc"><option value="">alle</option></select></label><label>Typ<select id="fType"><option value="">alle</option></select></label><label class="check"><input id="fSel" type="checkbox"> nur freigegebene</label></div>}
puts {<p class="hint">Häkchen setzen gibt den Wert sofort über Modbus frei. Format und Beschriftung kommen aus der CCU und lassen sich unter „Freigaben" ändern.</p>}
puts {<div id="deviceList" class="list"><div class="empty">Lade Geräteliste…</div></div>}
puts {</section>}

puts {<section id="tab-sysvars" class="tab hidden">}
puts {<div class="filters"><label>Name<input id="svName" type="search" placeholder="Systemvariable"></label><label class="check"><input id="svInternal" type="checkbox"> interne Variablen anzeigen</label></div>}
puts {<p class="hint">Angeboten werden Logik-, Zahlen- und Werteliste-Variablen. Zeichenketten werden nicht übertragen.</p>}
puts {<div id="sysvarList" class="list"></div>}
puts {</section>}

puts {<section id="tab-exports" class="tab hidden">}
puts {<p class="hint">Freigegebene Werte. Adressen bleiben fest; ein abgewählter Wert hinterlässt eine Lücke, damit sich bei anderen Werten nichts verschiebt.</p>}
puts {<div class="tablewrap"><table><thead><tr><th>Nr.</th><th>Bezeichnung</th><th>Quelle</th><th>Tabelle</th><th>Adresse</th><th>Format</th><th>Wert</th><th>Zustand</th><th></th></tr></thead><tbody id="exportRows"></tbody></table></div>}
puts {</section>}

puts {<section id="tab-sheet" class="tab hidden">}
puts {<div class="toolbar noprint"><div class="hint">Zum Einrichten des Modbus-Clients (SPS, Visualisierung, Gebäudeleittechnik).</div><span class="spacer"></span><button id="csvBtn">CSV speichern</button><button id="printBtn" class="primary">Drucken</button></div>}
puts {<div id="sheet"></div>}
puts {</section>}

puts {<section id="tab-settings" class="tab hidden">}
puts {<div class="card"><h3>Schreiben über Modbus</h3><p class="hint">Nur diese Geräte dürfen Werte schreiben (eine IP-Adresse oder ein Netz wie 192.168.1.0/24 je Zeile). Leer = niemand darf schreiben. Zusätzlich muss der einzelne Wert unter „Freigaben" als schreibbar markiert sein.</p>}
puts {<textarea id="allowList" rows="5" spellcheck="false"></textarea><div class="actions"><button id="saveAllow" class="primary">Speichern</button><span id="allowResult" role="status"></span></div></div>}
puts {<div class="card"><h3>Wortreihenfolge für 32-Bit-Werte</h3><p class="hint">ABCD (High-Word zuerst) ist Standard. CDAB nur, wenn der Client es verlangt.</p><select id="wordOrder"><option value="ABCD">ABCD (High-Word zuerst)</option><option value="CDAB">CDAB (Low-Word zuerst)</option></select></div>}
puts "<div class=\"card\"><h3>Port</h3><p class=\"hint\">Modbus-Standard ist 502. Beim Ändern startet der Server neu und trägt den neuen Port automatisch in die Firewall ein; bestehende Modbus-Verbindungen werden getrennt. Die Clients müssen danach den neuen Port verwenden.</p>"
puts "<div class=\"actions\"><input id=\"portInput\" type=\"number\" min=\"1\" max=\"65535\" value=\"[html $port]\"><button id=\"savePort\" class=\"primary\">Port ändern</button><span id=\"portResult\" role=\"status\"></span></div></div>"
puts {<div class="card"><h3>Letzte Schreibvorgänge</h3><div class="tablewrap"><table><thead><tr><th>Zeit</th><th>Client</th><th>Wert</th><th>Bezeichnung</th><th>Ergebnis</th></tr></thead><tbody id="writeLog"></tbody></table></div></div>}
puts {</section>}

puts {<footer class="copyright noprint">© 2026 Wolfram Henkel · CCU-ModbusServer-Lizenz 1.1</footer>}
puts {</main>}

puts {<dialog id="editDialog"><form method="dialog"><div class="dialoghead"><h2 id="eTitle">Freigabe</h2><button value="cancel" class="iconbtn" aria-label="Schließen">×</button></div>}
puts {<div class="grid"><label>Bezeichnung<input id="eLabel" maxlength="80"></label><label class="check"><input id="eWritable" type="checkbox"> über Modbus schreibbar</label></div>}
puts {<p id="eInfo" class="hint"></p>}
puts {<details><summary>Erweitert</summary><div class="grid advanced"><label>Format<select id="eFormat"></select></label><label>Skalierung (Registerwert = CCU-Wert × Faktor)<input id="eScale" type="number" step="any"></label></div><p class="hint">Für Ganzzahl-Formate: z. B. Faktor 10 überträgt 21,5 °C als 215.</p></details>}
puts {<div class="dialogactions"><button type="button" id="eRemove" class="danger">Freigabe entfernen</button><span class="spacer"></span><button value="cancel">Abbrechen</button><button type="button" id="eApply" class="primary">Übernehmen</button></div></form></dialog>}
puts "<script src=\"app.js?v=$v\"></script></body></html>"
