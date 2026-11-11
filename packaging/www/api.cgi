#!/bin/tclsh
# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        packaging/www/api.cgi
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
# Forwards requests of the add-on page to the daemon's local API
# (127.0.0.1 only). The browser never talks to the daemon directly.
#   GET  ?sid=..&path=status|catalog|mapping|sheet|writelog|config
#   POST ?sid=..&path=mapping|writeallow   (body: one JSON object)
#   POST ?sid=..&path=port   {"port":N}  handled here: bin/set-port, which
#        restarts the server (the daemon cannot restart itself)
# Protection: administrator session (like the OpenCCU add-on page), fixed
# path list, POST only with Content-Type application/json and an Origin
# (if the browser sends one) equal to this host. The Origin header is not
# forwarded; the daemon refuses requests that carry one.

source /usr/local/addons/ccu-modbusserver/lib/session.tcl
set cfgdir /usr/local/etc/config/addons/ccu-modbusserver

fconfigure stdout -translation binary -encoding binary

proc reply {code text body} {
    puts -nonewline stdout "Status: $code $text\r\nContent-Type: application/json; charset=utf-8\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\n\r\n$body"
    flush stdout
    exit 0
}

proc env {name} {
    if {[info exists ::env($name)]} { return $::env($name) }
    return ""
}

if {![check_session [query_value sid]]} {
    reply 403 Forbidden {{"error":"Sitzung ungültig. Bitte erneut über die OpenCCU anmelden."}}
}

set method [env REQUEST_METHOD]
set path [query_value path]
set get {status catalog mapping sheet writelog config}
set post {mapping writeallow port}
if {$method eq "GET" && [lsearch -exact $get $path] >= 0} {
    set body ""
} elseif {$method eq "POST" && [lsearch -exact $post $path] >= 0} {
    set ct [string tolower [env CONTENT_TYPE]]
    if {![regexp {^application/json\s*(;.*)?$} $ct]} {
        reply 415 {Unsupported Media Type} {{"error":"Content-Type muss application/json sein"}}
    }
    set origin [env HTTP_ORIGIN]
    if {$origin ne "" && $origin ne "http://[env HTTP_HOST]" && $origin ne "https://[env HTTP_HOST]"} {
        reply 403 Forbidden {{"error":"seitenfremde Anfrage"}}
    }
    set len [env CONTENT_LENGTH]
    if {![string is integer -strict $len] || $len <= 0 || $len > 16384} {
        reply 413 {Payload Too Large} {{"error":"ungültige Datengröße"}}
    }
    fconfigure stdin -translation binary -encoding binary
    set body [read stdin $len]
    if {[string length $body] != $len} {
        reply 400 {Bad Request} {{"error":"unvollständige Anfrage"}}
    }
} else {
    reply 405 {Method Not Allowed} {{"error":"Pfad oder Methode nicht erlaubt"}}
}

if {$path eq "port"} {
    if {![regexp {^\s*\{\s*"port"\s*:\s*([0-9]{1,5})\s*\}\s*$} $body all newport]} {
        reply 400 {Bad Request} {{"error":"erwartet {\"port\":Zahl}"}}
    }
    if {[catch {set res [exec /usr/local/addons/ccu-modbusserver/bin/set-port $newport]} err]} {
        # non-zero exit: the script's JSON line is the error text
        set res [lindex [split [string trim $err] "\n"] 0]
        if {![string match "\{*" $res]} { set res {{"ok":false,"port":0,"error":"Portänderung fehlgeschlagen"}} }
        reply 422 {Unprocessable Entity} $res
    }
    reply 200 OK $res
}

# API address from config.json; only the loopback interface is accepted
set port 18711
if {![catch {set f [open $cfgdir/config.json r]}]} {
    set cfg [read $f]
    close $f
    if {[regexp {"api_listen"\s*:\s*"([^"]*)"} $cfg all listen]} {
        if {![regexp {^127\.0\.0\.1:([0-9]{1,5})$} $listen all port]} {
            reply 500 {Internal Server Error} {{"error":"api_listen ist nicht 127.0.0.1"}}
        }
    }
}

if {[catch {
    set s [socket 127.0.0.1 $port]
    fconfigure $s -translation binary -encoding binary
    set req "$method /api/$path HTTP/1.0\r\nHost: 127.0.0.1\r\n"
    if {$method eq "POST"} {
        append req "Content-Type: application/json\r\nContent-Length: [string length $body]\r\n"
    }
    append req "\r\n$body"
    puts -nonewline $s $req
    flush $s
    # read with a time limit: a stalled daemon must not block the page
    fconfigure $s -blocking 0
    set ::resp ""
    set ::done ""
    fileevent $s readable [list apply {{s} {
        append ::resp [read $s]
        if {[eof $s]} { set ::done eof } elseif {[string length $::resp] > 33554432} { set ::done big }
    }} $s]
    set timer [after 20000 {set ::done timeout}]
    vwait ::done
    after cancel $timer
    close $s
} err]} {
    catch {close $s}
    reply 503 {Service Unavailable} {{"error":"CCU-ModbusServer läuft nicht"}}
}
if {$::done ne "eof"} {
    reply 504 {Gateway Timeout} {{"error":"CCU-ModbusServer antwortet nicht"}}
}
set resp $::resp

set sep [string first "\r\n\r\n" $resp]
if {$sep < 0 || ![regexp {^HTTP/1\.[01] ([0-9]{3}) ([^\r\n]*)} $resp all code text]} {
    reply 502 {Bad Gateway} {{"error":"ungültige Antwort des Servers"}}
}
reply $code $text [string range $resp [expr {$sep + 4}] end]
