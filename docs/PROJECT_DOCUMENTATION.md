# Kindle Spotify Remote - Projektdokumentation

## Zweck

Dieses Projekt ist eine KUAL-Extension fuer einen jailbroken Kindle, die Spotify vom Kindle aus anzeigen und steuern kann. Die App richtet sich auf Kindle Paperwhite 11. Generation / PW5 Geraete.

Private Geraetedaten wie Seriennummern, persoenliche E-Mail-Adressen und Registrierungsdaten gehoeren nicht in ein oeffentliches Repository. Fuer lokale Deployments koennen solche Daten in privaten Notizen ausserhalb von Git abgelegt werden.

Das Projekt enthaelt zwei Bedienkonzepte:

- Eine native Kindle-Vollbild-App mit Touch-Flaechen (`Touch Remote`).
- Eine passive e-ink Anzeige (`Now Playing Display`), die nur den aktuellen Titel anzeigt und keine Touch- oder Texteingabe annimmt.

Zusaetzlich liegt noch eine Browser-basierte Web-Remote im Projekt, die ueber einen lokalen HTTP-Server auf `127.0.0.1:8787` laufen kann.

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
  "eips_col_width": 22,
  "eips_row_height": 40,
  "button_top": 660,
  "button_height": 88,
  "button_gap": 2
}
```

Der wichtigste Fix fuer die Touch-Steuerung: Rohwerte werden nicht mehr nur dann skaliert, wenn sie groesser als die Bildschirmgroesse sind. Sie werden immer ueber `touch_min_*` bis `touch_max_*` in Bildschirmkoordinaten umgerechnet. Das ist wichtig, weil Kindle-Touchcontroller auch Rohwerte unterhalb der Displaybreite liefern koennen, die trotzdem aus einem 0..4095-Koordinatensystem stammen.

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
- Rohwerte muessen immer ueber `touch_min_*`, `touch_max_*`, `touch_swap_xy`, `touch_invert_x` und `touch_invert_y` kalibrierbar bleiben.
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

Es gibt ausserdem:

```text
extensions/spotifyremote/menu.json
```

Dieser zweite Ordner enthaelt eine gespiegelt benannte Menue-Datei. Er sollte synchron gehalten werden, falls KUAL oder eine Kopierhistorie noch auf `spotifyremote` statt `spotify-remote` zeigt.

Aktuelle Menuepunkte:

- `Touch Remote`: startet die native Vollbild-App mit Touch-Steuerung.
- `Now Playing Display`: startet die passive Vollbild-Anzeige.
- `Stop Now Playing Display`: stoppt die passive Anzeige.
- `Create Login URL`: schreibt eine Spotify Login-URL in `data/login_url.txt`.
- `Finish Login From callback.txt`: liest Redirect-URL oder Code aus `data/callback.txt` und tauscht den Code gegen Token.
- `Status`: schreibt den aktuellen Spotify-Status in `data/status.txt`.
- `Play / Pause`: sendet Play/Pause an Spotify.
- `Next`: naechster Titel.
- `Previous`: vorheriger Titel.
- `Volume +`: Lautstaerke um 10 erhoehen.
- `Volume -`: Lautstaerke um 10 senken.
- `Shuffle`: Shuffle umschalten.
- `Repeat`: Repeat-Modus umschalten.
- `Recover Kindle UI`: beendet laufende Prozesse und startet das Kindle-Framework wieder.

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

- `data/config.json`: Client ID, Redirect URI, Port, Refresh-Intervall.
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
6. Fuer Bedienung `Spotify Remote -> Touch Remote` starten.
7. Fuer reine Anzeige `Spotify Remote -> Now Playing Display` starten.

## Bedienung

### Empfohlener Weg

1. Auf Handy, Desktop oder Speaker Spotify starten.
2. KUAL auf dem Kindle oeffnen.
3. `Spotify Remote -> Touch Remote` starten.
4. Bei Bedarf Login ausfuehren.
5. Danach Play/Pause, Next, Previous, Volume, Shuffle, Repeat oder Devices nutzen.

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

### Tippen funktioniert nicht

Pruefen:

- Wurde `Touch Remote` gestartet und nicht `Now Playing Display`?
- Ist `bin/spotify-remote-arm` ausfuehrbar?
- Gibt es Eintraege in `logs/spotify-remote.log`?
- Zeigt die UI nach einem Tap `Tap ...` oder `Miss ...` an?
- Stimmen `raw=x,y` und `xy=x,y` im letzten Tap-Hinweis?
- Falls Rohwerte ankommen, aber `xy` falsch ist: `touch_min_*`, `touch_max_*`, `touch_swap_xy` und `touch_invert_*` in `data/config.json` anpassen.
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
