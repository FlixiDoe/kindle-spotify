# Kindle Spotify Remote - Projektdokumentation

## Zweck

Dieses Projekt ist eine KUAL-Extension fuer einen jailbroken Kindle, die Spotify vom Kindle aus anzeigen und steuern kann. Die App richtet sich auf Kindle Paperwhite 11. Generation / PW5 Geraete.

Private Geraetedaten wie Seriennummern, persoenliche E-Mail-Adressen und Registrierungsdaten gehoeren nicht in ein oeffentliches Repository. Fuer lokale Deployments koennen solche Daten in privaten Notizen ausserhalb von Git abgelegt werden.

Das Projekt enthaelt zwei Bedienkonzepte:

- Eine native Kindle-Vollbild-App mit Touch-Flaechen (`Touch Remote`).
- Eine passive e-ink Anzeige (`Now Playing Display`), die nur den aktuellen Titel anzeigt und keine Touch- oder Texteingabe annimmt.

Zusaetzlich liegt noch eine Browser-basierte Web-Remote im Projekt, die ueber einen lokalen HTTP-Server auf `127.0.0.1:8787` laufen kann.

## Entwicklungsunterstuetzung durch KI

Codex wurde als Main Agent fuer die Implementierung und Projektpflege genutzt, insbesondere mit GPT-5.4 und GPT-5.5 basierten Arbeitslaeufen.

Gemini wurde ergaenzend fuer externe Research-Arbeit genutzt, vor allem zu Kindle-, KUAL-, E-Ink-Rendering- und Cross-Compilation-Themen. Diese Research-Ergebnisse sind als Architektur-Input dokumentiert und werden von konkret verifiziertem Projektverhalten getrennt.

## Wichtige Pfade

```text
.
  readme
  docs/
    PROJECT_DOCUMENTATION.md
    crash-logs/
      README.md
      crash-log-*.log
  extensions/
    spotify-remote/
      config.xml
      menu.json
      launch.sh
      run-native.sh
      start.sh
      stop.sh
      recover.sh
      nowplaying-launch.sh
      nowplaying.sh
      nowplaying-stop.sh
      build.sh
      build.ps1
      README.md
      data/
        config.example.json
      logs/
        spotify-remote.log
      src/
        spotify-remote.go
        native/
          main.go
      www/
        index.html
        style.css
        app.js
    spotifyremote/
      config.xml
      menu.json
```

## Zielplattform

Die lauffaehige Extension ist fuer den Pfad auf dem Kindle gebaut:

```text
/mnt/us/extensions/spotify-remote
```

Der Ziel-Kindle ist ein Paperwhite 11. Generation. Die native App nimmt aktuell die Paperwhite-11/PW5-Anzeige als Grundlage und verwendet:

- Bildschirmbreite: `1236`
- Bildschirmhoehe: `1648`
- Touch-Rohbereich: `0..4095`
- eips-Zeilenraster: ca. `40 px` pro Zeile
- eips-Spaltenraster: ca. `22 px` pro Spalte

Diese Werte stehen in `extensions/spotify-remote/src/native/main.go`. Wenn Touch-Treffer oder Skalierung auf deinem konkreten Geraet noch daneben liegen, ist das der erste Ort fuer Anpassungen.

Die Werte koennen inzwischen ueber `extensions/spotify-remote/data/config.json` ueberschrieben werden:

```json
{
  "screen_width": 1236,
  "screen_height": 1648,
  "touch_min_x": 0,
  "touch_max_x": 4095,
  "touch_min_y": 0,
  "touch_max_y": 4095,
  "touch_swap_xy": false,
  "touch_invert_x": false,
  "touch_invert_y": false,
  "touch_use_kernel_abs": true,
  "eips_col_width": 22,
  "eips_row_height": 40,
  "button_top": 660,
  "button_height": 88,
  "button_gap": 2
}
```

Der wichtigste Fix fuer die Touch-Steuerung: Die App liest standardmaessig die echten ABS-Min/Max-Werte des jeweiligen `/dev/input/event*` per Kernel-`EVIOCGABS` aus. Dadurch funktioniert sie sowohl mit Touchcontrollern, die rohe Werte wie `0..4095` liefern, als auch mit Kindle/Firmware-Kombinationen, die bereits niedrigere Bildschirm- oder Framebuffer-nahe Koordinaten melden. Wenn die Kernelwerte auf einem Modell falsch sind, kann `"touch_use_kernel_abs": false` gesetzt werden; dann werden wieder `touch_min_*` und `touch_max_*` aus `data/config.json` verwendet.

## Externe Research-Notizen von Gemini

Dieser Abschnitt basiert auf einem externen Deep-Research-Text von Gemini 3.1 Pro mit erweitertem Thinking zum Thema Kindle-App-Entwicklung, KUAL, MRPI, LIPC, E-Ink-Rendering und Cross-Compilation. Die Notizen sind als Architektur-Input zu verstehen, nicht als vollstaendig verifizierte Projektspezifikation. Konkrete Zahlen aus dem Research, etwa RAM-Verbrauch, Latenzen oder Batteriewerte, sollten vor einer technischen Entscheidung auf echter Kindle-Hardware nachgemessen werden.

### Relevanz fuer dieses Projekt

Der Research bestaetigt die grundlegende Richtung dieser App:

- KUAL-Erweiterungen sollten klein bleiben und keinen permanenten Launcher-Daemon benoetigen.
- Lang laufende Hintergrundprozesse, Polling-Schleifen und haeufiges Logging sind auf Kindle-Geraeten kritisch, weil RAM, CPU, Akku und eMMC-Flash begrenzt sind.
- UI-Code sollte moeglichst direkt fuer E-Ink arbeiten. Browser- und High-Level-GUI-Stacks sind als Fallback nuetzlich, aber fuer eine robuste Vollbild-App schwerer kontrollierbar.
- Direkte Framebuffer- oder Kindle-native Ausgabe muss mit dem Amazon-Framework koordiniert werden, weil dieses den Bildschirm sonst uebermalen kann.
- Shell-Skripte, die Framework, Power-Management oder Displayzustand veraendern, muessen immer Recovery- und Cleanup-Pfade haben.

Die aktuelle Projektarchitektur folgt diesen Punkten bereits teilweise:

- Die Hauptbedienung liegt in einer kleinen nativen Go-Binary unter `src/native/main.go`.
- KUAL startet die App ueber `menu.json -> launch.sh -> run-native.sh -> bin/spotify-remote-arm`.
- `run-native.sh` setzt `preventScreenSaver`, stoppt das Kindle-Framework vor dem nativen Vollbildlauf und startet es beim Exit wieder.
- `recover.sh` ist der manuelle Notfallpfad, falls die Kindle-Oberflaeche nicht automatisch zurueckkommt.
- Die App liest Touch-Events direkt aus `/dev/input/event*` und normalisiert Rohkoordinaten ueber konfigurierbare Kalibrierungswerte.
- OAuth nutzt PKCE ohne Client Secret, weil der Kindle-Speicher als user-zugaenglich behandelt werden muss.

### KUAL- und Paketstruktur

Der Gemini-Research beschreibt KUAL-Erweiterungen als dateibasierte Module unter:

```text
/mnt/us/extensions/<extension-name>/
```

Klassische KUAL-Erweiterungen bestehen aus einer `config.xml` fuer Metadaten und einer `menu.json` fuer Menuepunkte. Dieses Projekt verwendet aktuell die `menu.json` als relevante KUAL-Definition. Wenn eine Firmware oder KUAL-Variante die Extension nicht erkennt, ist eine minimale `config.xml` der naechste sinnvolle Kompatibilitaetsschritt.

Wichtig fuer dieses Projekt:

- Der Zielpfad bleibt `/mnt/us/extensions/spotify-remote`.
- Die beiden Menue-Dateien `extensions/spotify-remote/menu.json` und `extensions/spotifyremote/menu.json` muessen synchron bleiben, solange beide Ordner im Repository existieren.
- Menueeintraege sollten knapp bleiben. Hauefig genutzte Aktionen gehoeren in die native Touch-App; KUAL-Menuepunkte bleiben fuer Start, Recovery, Login-Fallbacks und direkte Notaktionen sinnvoll.

### Framework- und Prozessmodell

Der Research hebt hervor, dass KUAL-Skripte als Kindprozesse des Amazon-Frameworks laufen koennen. Wenn das Framework zu frueh oder im gleichen Prozessbaum gestoppt wird, kann das gestartete Skript mit beendet werden.

Das Projekt loest das aktuell ueber einen zweistufigen Start:

```text
KUAL action -> launch.sh -> nohup sh run-native.sh -> native binary
```

`launch.sh` kehrt schnell zu KUAL zurueck, waehrend `run-native.sh` nach kurzer Wartezeit das Framework stoppt und die native App startet. Das entspricht dem Research-Prinzip der Prozessentkopplung. Falls auf einer Firmware trotzdem Prozessabbrueche auftreten, ist `setsid` als zusaetzliche Entkopplung in `launch.sh` oder `menu.json` zu pruefen.

Jede Aenderung an Framework-Steuerung muss diese Cleanup-Regeln einhalten:

- `preventScreenSaver` beim Start nur fuer aktive Vollbildsessions setzen.
- `preventScreenSaver` beim Exit oder Recovery wieder auf `0` setzen.
- Framework nach App-Ende wieder starten.
- PID-Dateien und `/tmp/spotify-remote.framework-stopped` aufraeumen.
- Keine kritischen Daemons wie Power-Management oder Netzwerk dauerhaft stoppen.

### LIPC und Energiemanagement

Gemini beschreibt LIPC als zentrale Kindle-Schnittstelle fuer Power-, Netzwerk- und UI-Dienste. Dieses Projekt nutzt LIPC aktuell bewusst schmal:

```sh
lipc-set-prop com.lab126.powerd preventScreenSaver 1
lipc-set-prop com.lab126.powerd preventScreenSaver 0
lipc-set-prop com.lab126.appmgrd start app://com.lab126.browser?url=...
```

Das ist fuer die Spotify-Remote angemessen. Weitere LIPC-Nutzung sollte nur hinzugefuegt werden, wenn sie einen klaren Zweck hat. Beispiele:

- WLAN-Reconnect nur dann, wenn echte Kindle-Logs zeigen, dass Spotify-Requests nach Suspend regelmaessig scheitern.
- RTC-Wakeup nur fuer ein spaeteres Always-On-Dashboard, nicht fuer die normale Touch-Remote.
- `lipc-wait-event` nur, wenn eine Funktion wirklich eventgetrieben statt pollend laufen kann.

Die aktive Touch-App darf alle paar Sekunden Spotify abfragen, weil sie eine Vordergrund-App ist. Die passive Anzeige sollte dagegen sparsam bleiben. Ein dauerhaftes Now-Playing-Dashboard sollte langfristig nicht mit engen `sleep`-Pollingzyklen laufen, sondern mit laengeren Intervallen, manueller Aktualisierung oder einem RTC-Wakeup-Design.

### Framework Start/Stop auf PW5

Auf neueren Kindle-5.x-Firmwares, inklusive PW5-Klasse, ist `start framework` / `stop framework` der primaere Upstart-Pfad. Aeltere Anleitungen und manche Erweiterungen referenzieren noch `/etc/init.d/framework`; die bisherigen Crashlogs zeigten aber, dass dieser Pfad auf dem getesteten Geraet nicht existiert.

Projektregel:

- `run-native.sh` verwendet zuerst `stop framework` und faellt nur auf `/etc/init.d/framework stop` zurueck, wenn die Datei existiert und ausfuehrbar ist.
- `run-native.sh`, `stop.sh` und `recover.sh` verwenden fuer Recovery zuerst `start framework`.
- Fehlendes `/etc/init.d/framework` ist auf PW5 kein App-Fehler, solange `start framework` / `stop framework` funktioniert.

### E-Ink-Rendering: eips und fbink

Der Research empfiehlt `fbink` als robustere Framebuffer-Schicht fuer E-Ink. Dieses Projekt nutzt derzeit zwei Wege:

- Native Touch-App: `eips`
- Now Playing Display: bevorzugt `fbink`, mit Fallback ohne Zeichnung, wenn kein `fbink` gefunden wird

`eips` ist auf Kindle-Geraeten naheliegend und fuer textbasierte Bedienung ausreichend. Es ist aber grob gerastert und bietet weniger Kontrolle ueber Refresh-Verhalten, Schriftgroesse, Bilder und partielles Rendering. `fbink` ist fuer eine bessere passive Anzeige und spaetere Cover-/Dashboard-Ansichten geeigneter.

Praktische Regel:

- Touch-Remote: `eips` behalten, solange Stabilitaet und Lesbarkeit wichtiger sind als visuelle Qualitaet.
- Now Playing Display: `fbink` bevorzugen und dessen Vorhandensein sauber dokumentieren.
- Keine Python/Pillow/PyQt-UI fuer die Haupt-App einfuehren, solange die App auf dem Kindle selbst laufen soll.

### Touch- und Eingabemodell

Der Research beschreibt die direkte Auswertung von Linux-`evdev`-Events. Genau das macht `src/native/main.go`.

Wichtig fuer Wartung und Portierung:

- Nicht annehmen, dass `/dev/input/event1` immer der Touchscreen ist.
- Das aktuelle Scannen von `/dev/input/event0` bis `/dev/input/event11` ist bewusst defensiv.
- Die App sollte zuerst die Kernel-ABS-Ranges des Event-Devices verwenden und nur bei Bedarf auf manuelle `touch_min_*`/`touch_max_*`-Werte zurueckfallen.
- Rohwerte muessen weiterhin ueber `touch_min_*`, `touch_max_*`, `touch_swap_xy`, `touch_invert_x` und `touch_invert_y` kalibrierbar bleiben.
- Neue Kindle-Modelle koennen andere Event-Codes oder Achsenbereiche liefern.
- Die UI sollte immer Tap-Diagnosen wie `raw=x,y xy=x,y` anzeigen oder loggen, damit Kalibrierung ohne Debugger moeglich bleibt.

### Cross-Compilation und Firmware-Unterschiede

Der Gemini-Research weist auf Architekturunterschiede im Kindle-Oekosystem hin, besonders ARMv6/ARMv7 sowie Softfloat/Hardfloat bei neueren Firmwares. Fuer dieses Projekt ist das Risiko kleiner als bei C/C++-Programmen, weil die native App aktuell mit:

```text
GOOS=linux
GOARCH=arm
GOARM=7
CGO_ENABLED=0
```

gebaut wird. `CGO_ENABLED=0` vermeidet dynamische Abhaengigkeiten gegen Kindle-Systembibliotheken weitgehend.

Trotzdem gelten diese Regeln:

- `GOARM=7` ist der Standard fuer PW5-nahe Geraete.
- `GOARM=6` ist der erste Fallback fuer aeltere Kindle-Modelle.
- Keine CGO-Abhaengigkeiten einfuehren, solange sie nicht zwingend notwendig sind.
- Wenn native C-Tools wie `fbink`, `jq` oder `curl` gebuendelt werden, muessen sie fuer das passende Kindle-Ziel gebaut oder aus vertrauenswuerdigen Kindle-Paketen uebernommen werden.
- Firmware `>= 5.16.3` kann Hardfloat-spezifische Probleme bei externen Binaries verursachen; reine Go-Binaries ohne CGO sind weniger anfaellig, aber reale Tests bleiben notwendig.

### Logging, Flash-Schreibzugriffe und lokale Daten

Der Research warnt vor haeufigem Schreiben auf den internen Flash. Dieses Projekt schreibt Logs nach:

```text
extensions/spotify-remote/logs/spotify-remote.log
```

Das ist fuer Debugging hilfreich, sollte aber nicht unkontrolliert wachsen.

Empfohlene Regeln:

- Logs fuer reale Kindle-Tests kurz halten und nach Fehleranalyse loeschen oder gezielt nach `docs/crash-logs/` uebernehmen.
- Keine hochfrequenten Debug-Logs in Touch- oder Refresh-Schleifen dauerhaft aktiv lassen.
- Tokens, lokale Configs und Runtime-Logs nicht committen.
- Temporare Dateien bevorzugt in `data/` oder `/tmp` halten, je nach Lebensdauer und Recovery-Bedarf.

### Research-Input, der nicht blind uebernommen wird

Einige Aussagen aus dem Gemini-Text sind als Richtung plausibel, aber fuer dieses Projekt nicht direkt belastbar:

- Exakte RAM-, Latenz-, Batterieverbrauchs- und Fragmentierungszahlen werden nicht als Projektfakten dokumentiert, solange sie nicht auf dem Ziel-Kindle gemessen wurden.
- Aussagen zu spezifischen Hotfix-Interna, MKK-Keys, `gandalf` oder `appreg.db` sind fuer diese App nicht notwendig und werden nicht als Voraussetzung aufgenommen.
- Always-On-RTC-Wakeup-Design ist fuer eine Spotify-Fernbedienung aktuell Overengineering. Es wird erst relevant, wenn das Projekt zu einem dauerhaft laufenden Dashboard ausgebaut wird.
- Eine vollstaendige Abkehr von `eips` ist nicht erforderlich. `fbink` ist ein guter Ausbaupfad, aber keine harte Voraussetzung fuer die aktuelle Touch-Remote.

## Gepruefte Kindle-/KUAL-Dokumentation

Dieser Abschnitt fasst die zusaetzlich geprueften Doku-Startpunkte zusammen. Die Links stammen aus der bereitgestellten Doku-Liste und wurden gegen die Projektarchitektur abgeglichen.

### MobileRead Kindle Developer's Corner

MobileRead bleibt der wichtigste Community-Ort fuer Kindle-Jailbreak, KUAL, MRPI, USBNetwork, native Tools und historische Kindle-Hacks. Fuer dieses Projekt ist MobileRead vor allem relevant, wenn KUAL selbst nicht startet, Menues nicht angezeigt werden oder ein Firmware-/DevCert-/MKK-Problem vorliegt.

Projektregel:

- App-Bugs zuerst ueber `logs/spotify-remote.log`, `status.txt` und KUAL-Menuepfade debuggen.
- KUAL-Installations- oder Signaturprobleme getrennt davon behandeln; diese liegen meist ausserhalb der Spotify-App.

Quelle: `https://www.mobileread.com/forums/forumdisplay.php?f=150`

### KindleModding: KUAL und MRPI

Die KindleModding-Doku bestaetigt den modernen Installationspfad fuer Homebrew: KUAL und MRPI installieren, `extensions` und `mrpackages` korrekt auf den Kindle kopieren und MRPI ueber `;log mrpi` starten. Sie weist auch auf freien Speicher, Airplane Mode beim Speicher-Fuellen und Dateinamenprobleme bei Downloads hin.

Fuer dieses Projekt bedeutet das:

- Das Release-Paket muss einen direkt kopierbaren Ordner `spotify-remote` fuer `/mnt/us/extensions/` enthalten.
- Die ARM-Binary muss im Release-ZIP enthalten und auf dem Kindle ausfuehrbar sein.
- Installationsprobleme wie fehlendes KUAL, fehlendes MRPI oder falsche KUAL-Variante sind nicht durch App-Code zu loesen.

Quelle: `https://kindlemodding.org/jailbreaking/post-jailbreak/installing-kual-mrpi/`

Weitere relevante KindleModding-Startpunkte:

- `https://kindlemodding.org/kindle-dev/`
- `https://kindlemodding.org/kindle-dev/kindle-sdk.html`
- `https://kindlemodding.org/jailbreak-faq.html`

### KUAL Booklet README

Das KUAL-Booklet-README von NiLuJe bestaetigt, dass KUAL als Launcher fuer viele Kindle-Generationen gepflegt wird, inklusive PW5-Klasse. Es ist damit die richtige Laufzeitannahme fuer diese App, solange der Kindle bereits jailbroken ist und KUAL funktioniert.

Quelle: `https://github.com/NiLuJe/KUAL_Booklet/blob/master/README.txt`

### WebLaunch

WebLaunch ist eine KUAL-Extension, die URLs auf Kindle Touch/PaperWhite ohne normalen Browserrahmen oeffnet und dadurch eine Web-App nativer wirken laesst. Das passt als Alternativpfad zur vorhandenen Browser-Remote, aber nicht als Ersatz fuer die aktuelle native Touch-App.

Bewertung fuer Spotify Remote:

- Gut fuer einen spaeteren UI-Fallback: lokaler HTTP-Server plus rahmenlose WebLaunch-Ansicht.
- Riskant als Haupt-UI, weil Kindle-WebKit alt ist und Spotify/OAuth/modernes JavaScript begrenzt laufen koennen.
- Sinnvoll als Debug-/Fallback-Modus, wenn native evdev-Touch-Steuerung auf einem Modell noch Probleme macht.

Quelle: `https://github.com/PaulFreund/WebLaunch`

### KOReader Developer Docs

KOReader ist fuer dieses Projekt keine direkte Zielplattform, aber eine gute Referenz fuer robuste Kindle-nahe UI- und Input-Architektur. Die KOReader-Doku beschreibt den Frontend-Teil als Lua-basiert und verweist auf eine eigene Widget-/Input-/Device-Schicht.

Projektableitung:

- KOReader-Plugin ist fuer diese Spotify-Remote aktuell nicht der beste erste Weg.
- KOReader bleibt aber eine gute Referenz fuer Touch-Handling, E-Ink-UI, Power-Management und Firmware-Unterschiede.
- Wenn die App spaeter als KOReader-Plugin gedacht wird, waere die Struktur `plugin.koplugin/_meta.lua` und `main.lua` ein separater Architekturpfad.

Quelle: `https://koreader.rocks/doc/topics/Development_guide.md.html`

### Renderer-, LIPC- und Input-Referenzen

Die folgenden Referenzen sind direkt an konkrete Dateien im Projekt gekoppelt:

- `https://wiki.mobileread.com/wiki/Eips`: Referenz fuer `eips -c` und `eips row col text` in `src/native/main.go`, `launch.sh`, `recover.sh` und Fehleranzeigen.
- `https://github.com/NiLuJe/FBInk`: Referenz fuer den geplanten besseren E-Ink-Renderer im `nowplaying.sh`-Pfad.
- `https://www.mobileread.com/forums/showthread.php?t=299620`: MobileRead-Thread zu FBInk und Kindle-spezifischen Details.
- `https://wiki.mobileread.com/wiki/Lipc`: Referenz fuer `lipc-set-prop`, `lipc-get-prop` und Services wie `com.lab126.powerd` / `com.lab126.appmgrd`.
- `https://wiki.mobileread.com/wiki/Kindle_Touch_Hacking`: Kontext zu Kindle Touch, LIPC und Framework-Management.
- `https://www.kernel.org/doc/Documentation/input/event-codes.txt`: Referenz fuer `EV_ABS`, `EV_KEY`, `EV_SYN`, `ABS_X/Y`, `ABS_MT_POSITION_X/Y`.
- `https://www.kernel.org/doc/Documentation/input/multi-touch-protocol.txt`: Referenz fuer Multitouch-Release per `ABS_MT_TRACKING_ID = -1`.
- `https://www.mobileread.com/forums/showthread.php?t=225149`: Kontext zu LIPC-Daemon und Framework-/Firmware-Unterschieden.

### KUAL-Struktur aus Referenzprojekten

KOReader, WebLaunch und kleinere KUAL-Projekte verwenden typischerweise `config.xml` plus `menu.json`. Deshalb enthaelt dieses Projekt jetzt fuer beide vorhandenen Extension-Ordner eine minimale `config.xml`:

```text
extensions/spotify-remote/config.xml
extensions/spotifyremote/config.xml
```

Die `id` muss zum jeweiligen Ordnernamen passen:

```text
spotify-remote -> <id>spotify-remote</id>
spotifyremote  -> <id>spotifyremote</id>
```

Die Menuequelle bleibt jeweils:

```xml
<menu type="json" dynamic="true">menu.json</menu>
```

## Architektur

### Native Touch Remote

Datei: `extensions/spotify-remote/src/native/main.go`

Das native Go-Programm ist der wichtigste App-Teil fuer die Bedienung auf dem Kindle. Es:

- zeichnet die UI per `eips`;
- liest Touch-Ereignisse direkt aus `/dev/input/event*`;
- normalisiert Touch-Koordinaten;
- steuert Spotify ueber die Web API;
- speichert OAuth-Token lokal;
- stellt KUAL-Unterbefehle fuer einzelne Aktionen bereit.

Der KUAL-Menueintrag `Touch Remote` startet diese App ueber:

```text
menu.json -> launch.sh -> run-native.sh -> bin/spotify-remote-arm
```

`run-native.sh` stoppt vor dem Start das Kindle-Framework, damit die native eips-UI nicht von der normalen Kindle-Oberflaeche uebermalt wird. Beim Beenden startet es das Framework wieder.

Der Launcher bevorzugt `bin/spotify-remote-arm.new`, wenn diese Datei existiert. Dadurch kann ein neuer Build per USB bereitgestellt werden, ohne die moeglicherweise noch laufende oder vom Kindle gelockte Datei `bin/spotify-remote-arm` direkt zu ersetzen.

Auf neueren Kindle/PW5-Firmwares kann der Framework-Rueckweg nach dem Beenden visuell haengen bleiben, obwohl der native Prozess beendet ist. Typische Symptome sind weisser Screen, ein halb gezeichneter Startbildschirm oder ein scheinbar eingefrorenes Display direkt nach dem Schliessen der Spotify-App. Der dokumentierte erste Recovery-Schritt ist dann ausdruecklich kein Reboot: unteren physischen Display-/Power-Knopf einmal druecken, Display ausschalten lassen und den Kindle wieder wecken. Das triggert in der Praxis den saubersten Framework-Redraw. Wenn das nicht reicht, `Recover Kindle UI` in KUAL verwenden. Reboot bleibt der letzte Ausweg.

### Now Playing Display

Dateien:

- `extensions/spotify-remote/nowplaying-launch.sh`
- `extensions/spotify-remote/nowplaying.sh`
- `extensions/spotify-remote/nowplaying-stop.sh`

Diese Anzeige ist bewusst passiv. Sie aktualisiert alle 8 Sekunden den aktuellen Titel und zeichnet eine grosse e-ink Anzeige mit Titel, Kuenstler, Album, Fortschritt, Lautstaerke, Shuffle und Repeat.

Wichtig: Diese Anzeige verarbeitet keine Touch-Eingaben. Steuerung laeuft ueber KUAL-Menuepunkte oder ueber `Touch Remote`.

### Browser Remote

Dateien:

- `extensions/spotify-remote/src/spotify-remote.go`
- `extensions/spotify-remote/www/index.html`
- `extensions/spotify-remote/www/style.css`
- `extensions/spotify-remote/www/app.js`

Die Browser-Remote startet einen lokalen Server auf:

```text
http://127.0.0.1:8787
```

Sie stellt eine einfache HTML-Oberflaeche bereit. Der Server proxy't Spotify API-Aufrufe, damit OAuth, Token-Refresh und Playback-Control trotz Kindle-Browser-Limitierungen funktionieren.

Dieser Pfad ist aktuell weniger zentral als die native Touch-App, bleibt aber als Fallback und Entwicklungsoberflaeche im Projekt.

## KUAL-Menue

Die Hauptdefinition liegt in:

```text
extensions/spotify-remote/menu.json
```

Die zugehoerige KUAL-Metadatei liegt in:

```text
extensions/spotify-remote/config.xml
```

Es gibt ausserdem:

```text
extensions/spotifyremote/menu.json
extensions/spotifyremote/config.xml
```

Dieser zweite Ordner enthaelt gespiegelt benannte KUAL-Dateien. Er sollte synchron gehalten werden, falls KUAL oder eine Kopierhistorie noch auf `spotifyremote` statt `spotify-remote` zeigt.

Aktuelle Menuepunkte:

- `Now Playing Display`: startet die Kindle-Vollbild-App.
- `Create Login URL`: schreibt eine Spotify Login-URL in `data/login_url.txt`.
- `Finish Login From callback.txt`: liest Redirect-URL oder Code aus `data/callback.txt` und tauscht den Code gegen Token.

Notfall- und Direktsteuerungs-Kommandos wie Stop, Status, Play/Pause, Next, Previous und Recover bleiben als Skripte/CLI-Funktionen im Projekt vorhanden, sind aber aus dem normalen KUAL-Menue ausgeblendet. Der User sieht damit nur Start und Login.

Auf dem getesteten Kindle wurde ausserdem ein Kompatibilitaets-Fallback ueber `extensions/kindlefetch/menu.json` verwendet: Dort kann ein einzelner `Spotify Remote`-Ordner eingetragen sein, falls KUAL die dedizierten Spotify-Extension-Ordner nicht anzeigt. Dieser Fallback darf nur den Ordner enthalten, nicht wieder flache `Spotify ...`-Eintraege in der KUAL-Hauptliste.

## Spotify OAuth

Die App nutzt OAuth PKCE ohne Client Secret. Das ist richtig fuer ein Geraet wie den Kindle, weil ein Client Secret dort nicht sicher geheim gehalten werden kann.

Spotify Developer Dashboard:

```text
https://developer.spotify.com/dashboard
```

Redirect URI:

```text
http://127.0.0.1:8787/callback
```

Scopes:

```text
user-read-playback-state user-modify-playback-state user-read-currently-playing
```

Wichtige Dateien:

- `data/config.json`: lokale User-Konfiguration mit eigener Spotify Client ID, Redirect URI, Port, Refresh-Intervall. Diese Datei wird nicht committed.
- `data/config.example.json`: public-sichere Vorlage ohne echte Client ID.
- `data/oauth.json`: temporaerer PKCE State und Code Verifier waehrend Login.
- `data/token.json`: Access Token, Refresh Token und Ablaufzeit.
- `data/callback.txt`: manueller Login-Fallback fuer Redirect-URL oder Code.
- `data/login_url.txt`: Login-URL fuer Login ueber anderes Geraet.

## Crashlogs und lokale Daten

Historische Crashlogs, die fuer Debugging relevant sind, liegen unter:

```text
docs/crash-logs/
```

Ad-hoc Crashlogs auf Repository-Root-Ebene bleiben ignoriert, bis sie geprueft und bewusst nach `docs/crash-logs/` verschoben werden.

## Lokale Daten und Sicherheit

Diese Dateien enthalten lokale oder private Daten:

- `extensions/spotify-remote/data/token.json`
- `extensions/spotify-remote/data/config.json`
- `extensions/spotify-remote/logs/spotify-remote.log`

`token.json` enthaelt Spotify-Zugriffsdaten. Diese Datei sollte nicht oeffentlich geteilt werden. Wenn das Repository irgendwann auf GitHub soll, sollten Token, Logs und personenbezogene Geraetedaten vorher entfernt oder in `.gitignore` ausgelagert werden.

Die App erzeugt `data/config.json` automatisch aus sicheren Defaults, falls die Datei fehlt. Public-Nutzer muessen danach nur `client_id` mit ihrer eigenen Spotify Developer App Client ID ersetzen. Ein Client Secret darf nicht eingetragen werden, weil der Kindle-Speicher nicht als geheim gelten kann und OAuth PKCE genau diesen Public-Client-Fall abdeckt.

## Build

Voraussetzung auf dem PC:

- Go installiert
- PowerShell unter Windows oder Shell unter Linux/macOS

Windows:

```powershell
cd extensions\spotify-remote
.\build.ps1
```

Linux/macOS:

```sh
cd extensions/spotify-remote
./build.sh
```

Build-Ziel:

```text
GOOS=linux
GOARCH=arm
GOARM=7
CGO_ENABLED=0
```

Ergebnis:

```text
extensions/spotify-remote/bin/spotify-remote-arm
```

Falls das Binary auf einem anderen Kindle nicht startet, ist `GOARM=6` der naheliegende Fallback.

## Packaging

Der kopierbare Extension-Ordner ist:

```text
extensions/spotify-remote
```

PowerShell:

```powershell
Compress-Archive -Path extensions\spotify-remote -DestinationPath spotify-remote-kual.zip -Force
Compress-Archive -Path extensions\spotify-remote -DestinationPath spotify-remote-native-kual.zip -Force
```

Die beiden ZIPs enthalten aktuell denselben Extension-Ordner. Wenn nur ein Paket gebraucht wird, reicht `spotify-remote-kual.zip`.

## Installation auf dem Kindle

1. Kindle per USB verbinden.
2. Den Ordner `extensions/spotify-remote` oder den ZIP-Inhalt nach `/mnt/us/extensions/spotify-remote` kopieren.
3. Sicherstellen, dass Shell-Skripte und Binary ausfuehrbar sind:

```sh
chmod 755 /mnt/us/extensions/spotify-remote/*.sh
chmod 755 /mnt/us/extensions/spotify-remote/bin/spotify-remote-arm
```

4. Kindle auswerfen.
5. KUAL oeffnen.
6. `Spotify Remote -> Now Playing Display` starten.

## Bedienung

### Empfohlener Weg

1. Auf Handy, Desktop oder Speaker Spotify starten.
2. KUAL auf dem Kindle oeffnen.
3. `Spotify Remote -> Now Playing Display` starten.
4. Bei Bedarf Login ausfuehren.
5. Danach Play/Pause, Next, Previous, die mittigen `VOL- xx% VOL+` Touchflaechen sowie die `SHUF`/`REP` Statuslabels fuer Shuffle und Repeat nutzen. Repeat schaltet `off`, `context` und `track` zyklisch durch.

### Manueller Login-Fallback

Wenn der Kindle-Browser Spotify Login oder Localhost-Redirect nicht sauber schafft:

1. In KUAL `Create Login URL` ausfuehren.
2. `data/login_url.txt` oeffnen und URL auf Handy/PC nutzen.
3. Nach Spotify Login die Redirect-URL oder nur den `code` kopieren.
4. Den Wert in `data/callback.txt` eintragen.
5. In KUAL `Finish Login From callback.txt` ausfuehren.

## Troubleshooting

### Display sieht klein aus

Das Foto aus der Fehleranalyse zeigte die passive `Now Playing Display`-Ansicht. Diese wurde groesser skaliert und als passive Anzeige beschriftet. Fuer Touch-Bedienung muss `Touch Remote` gestartet werden.

Der spaetere Versuch, das Now-Playing-Layout deutlich groesser ueber das ganze Display zu ziehen, wurde wieder zurueckgenommen, weil er das Layout auf echter Hardware zerstoert hat. Der aktuelle Stand ist der zuvor funktionierende kompaktere FBInk-Screen mit Albumcover, Titel/Artist/Album, Fortschritt und grossen Touchzonen.

### Tippen funktioniert nicht

Pruefen:

- Wurde `Touch Remote` gestartet und nicht `Now Playing Display`?
- Ist `bin/spotify-remote-arm` ausfuehrbar?
- Gibt es Eintraege in `logs/spotify-remote.log`?
- Zeigt die UI nach einem Tap `Tap ...` oder `Miss ...` an?
- Stimmen `raw=x,y` und `xy=x,y` im letzten Tap-Hinweis?
- Falls Rohwerte ankommen, aber `xy` falsch ist: `touch_min_*`, `touch_max_*`, `touch_swap_xy` und `touch_invert_*` in `data/config.json` anpassen.
- Falls die Kernel-ABS-Erkennung falsche Bereiche meldet: `"touch_use_kernel_abs": false` setzen und die manuellen Touchbereiche verwenden.
- Falls `xy` stimmt, aber die sichtbaren Zeilen verschoben sind: `eips_row_height`, `button_top`, `button_height` und `button_gap` anpassen.

Interpretation der Touch-Diagnose:

```text
Tap PLAY / PAUSE raw=2048,3100 xy=618,1247
Miss raw=800,1200 xy=241,482
```

- `raw` sind die Touchcontroller-Werte aus `/dev/input/event*`.
- `xy` sind die normalisierten Bildschirmkoordinaten.
- `Tap ...` bedeutet: ein Button-Hit wurde gefunden und die Aktion wurde ausgeloest.
- `Miss ...` bedeutet: Touch kam an, lag nach Normalisierung aber ausserhalb aller Button-Flaechen.

### Kindle UI bleibt haengen

KUAL-Menuepunkt `Recover Kindle UI` ausfuehren. Alternativ per Shell:

```sh
sh /mnt/us/extensions/spotify-remote/recover.sh
```

### Kein aktives Spotify-Geraet

Spotify auf einem anderen Geraet starten und kurz abspielen. Danach `Refresh` oder `Status` verwenden.

### Premium Required

Spotify Playback-Control-Endpunkte verlangen in der Praxis Spotify Premium. Anzeige kann teilweise funktionieren, Steuerung aber nicht.

### Network blocked

Pruefen:

- Kindle-WLAN verbunden?
- DNS/Router blockiert `accounts.spotify.com` oder `api.spotify.com`?
- Captive Portal aktiv?
- AdGuard/Pi-hole blockiert Spotify?

## Entwicklungsnotizen

- Nach jeder abgeschlossenen Aenderung soll ein Git-Commit erstellt werden.
- Jeder Commit soll den ausfuehrenden AI-Agenten im Commit-Body nennen, z. B. `Agent: codex`.
- Manuelle Aenderungen des Nutzers duerfen nicht ungefragt verworfen werden.
- Vor Builds zuerst `git status --short` pruefen.
- Nach Menue-Aenderungen beide Menue-Dateien synchron halten:
  - `extensions/spotify-remote/menu.json`
  - `extensions/spotifyremote/menu.json`
- Nach KUAL-Metadaten-Aenderungen beide `config.xml`-Dateien pruefen:
  - `extensions/spotify-remote/config.xml`
  - `extensions/spotifyremote/config.xml`
- Nach Native-Aenderungen `build.ps1` oder `build.sh` ausfuehren und die ZIPs neu erzeugen.

## Verifikationsbefehle

```powershell
git status --short
python -m json.tool extensions\spotify-remote\menu.json > $null
python -m json.tool extensions\spotifyremote\menu.json > $null
cd extensions\spotify-remote
.\build.ps1
$env:GO111MODULE='off'; go test ./src/native
```

## Bekannte Grenzen

- Echte Touch- und eips-Verifikation braucht den physischen Kindle.
- Der Kindle Experimental Browser ist alt und fuer OAuth nicht immer verlaesslich.
- Die native App greift direkt auf `/dev/input/event*` zu; Kindle-Firmware-Unterschiede koennen Event-Codes oder Rohwerte beeinflussen.
- `eips`-Rendering ist geraete- und firmwareabhaengig.
- Lokale Daten, Tokens, Logs, ZIPs und Binaries sollen nicht veroeffentlicht werden. Die `.gitignore` schliesst diese Artefakte fuer neue Aenderungen aus.
