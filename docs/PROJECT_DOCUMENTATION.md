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
