# Kindle Spotify Remote

Fertige KUAL-Extension fuer einen jailbroken Kindle Paperwhite 11 / PW5 / 2021. Die App startet einen lokalen Webserver auf `127.0.0.1:8787`, speichert Spotify OAuth-PKCE-Tokens lokal und zeigt eine e-ink-freundliche Spotify-Remote im Kindle Experimental Browser.

## Architekturentscheidung

Gewaehlt ist D: KUAL startet einen lokalen Mini-Server und oeffnet den Kindle Browser.

Warum nicht reine lokale HTML/JS-App: `file://` plus Spotify Web API ist wegen CORS, PKCE-Redirect und Token-Speicherung auf dem Kindle-Browser unzuverlaessig. Der lokale Server macht OAuth, Token-Refresh, Spotify API Proxy und statisches UI ohne Client Secret. Auf dem Kindle muessen keine Pakete, Compiler, KTerm, USBNetwork, SSH oder externe Server installiert werden.

## Projektstruktur

```text
extensions/spotify-remote/
  menu.json
  start.sh
  stop.sh
  bin/
    spotify-remote-arm
  www/
    index.html
    style.css
    app.js
  data/
    config.json
    token.json
  logs/
    spotify-remote.log
  src/
    spotify-remote.go
  build.sh
  build.ps1
  README.md
```

## Spotify Developer Setup

1. Oeffne <https://developer.spotify.com/dashboard>.
2. Erstelle eine App.
3. Setze als Redirect URI exakt:

```text
http://127.0.0.1:8787/callback
```

4. Kopiere die Client ID.
5. Trage sie auf dem Kindle in der Setup-Maske ein oder direkt in `data/config.json`.
6. Kein Client Secret verwenden.

## Build / Cross-Compile

Auf einem PC mit Go:

```sh
cd extensions/spotify-remote
./build.sh
```

Windows PowerShell:

```powershell
cd extensions\spotify-remote
.\build.ps1
```

Das erzeugt `bin/spotify-remote-arm` fuer Linux ARMv7 (`GOOS=linux GOARCH=arm GOARM=7`) ohne CGO. Falls dein Toolchain-Ziel fuer einen anderen Kindle angepasst werden muss, ist `GOARM=6` der naheliegende Fallback.

## Packaging

Der kopierbare Ordner ist `extensions/spotify-remote`. ZIP aus dem Repository-Wurzelordner:

```sh
zip -r spotify-remote-kual.zip extensions/spotify-remote
```

PowerShell:

```powershell
Compress-Archive -Path extensions\spotify-remote -DestinationPath spotify-remote-kual.zip -Force
```

## Installation auf dem Kindle

1. Kindle per USB verbinden.
2. Den Ordner `spotify-remote` nach `/extensions/spotify-remote` auf dem Kindle kopieren.
3. Kindle auswerfen.
4. KUAL oeffnen.
5. `Start Spotify Remote` waehlen.
6. Falls der Browser nicht automatisch oeffnet: `Open Spotify Remote` waehlen oder im Experimental Browser `http://127.0.0.1:8787` oeffnen.

## OAuth Login

Die App generiert einen PKCE Code Verifier, oeffnet Spotify Login und erwartet den Redirect auf:

```text
http://127.0.0.1:8787/callback
```

Token werden in `data/token.json` gespeichert und automatisch erneuert. Wenn der Kindle-Browser den Localhost-Redirect nicht schafft, bleibt in der UI der Bereich `Manual Login Fallback` sichtbar. Dann die Redirect-URL oder nur den `code` in die Box einfuegen und `Finish Login` tippen.

## KUAL Menue

- Start Spotify Remote: startet den Server und versucht den Browser zu oeffnen.
- Open Spotify Remote: startet bei Bedarf und oeffnet `http://127.0.0.1:8787`.
- Stop Spotify Remote: beendet den Server ueber PID-Datei.
- Show Log: versucht `logs/spotify-remote.log` im Browser zu oeffnen.

## Funktionen

- OAuth Login mit PKCE ohne Client Secret
- Lokale Token-Speicherung
- Automatischer Refresh Token Flow
- Aktueller Song: Titel, Kuenstler, Album, Fortschritt, Play/Pause, optional Cover
- Controls: Play/Pause, Next, Previous, Volume +/-, Shuffle, Repeat
- Geraete anzeigen und aktives Geraet wechseln
- Refresh alle 8 Sekunden
- Klare Fehlertexte fuer Playback, Token, Premium, Netzwerk und aktive Geraete

## Troubleshooting

`Failed to get playback state`: Spotify `/me/player` ist nicht erreichbar oder gibt keinen gueltigen Playback-State zurueck. Pruefe Login, Netzwerk und aktives Geraet.

`No active Spotify device`: Starte Spotify auf Handy, Desktop oder Speaker und spiele kurz etwas ab. Danach `Refresh` tippen oder ein Geraet in `Devices` auswaehlen.

`Token expired`: Login erneut starten. Wenn Refresh dauerhaft scheitert, loesche `data/token.json` und logge dich neu ein.

`Premium required`: Spotify verlangt Premium fuer Playback-Control-Endpunkte. Track-Anzeige kann teilweise funktionieren, Steuerung aber nicht.

`Network blocked`: Kindle-WLAN, DNS, AdGuard/Pi-hole, Router-Firewall oder Captive Portal blockieren `accounts.spotify.com`, `api.spotify.com` oder Cover-CDNs.

Kindle Browser laedt localhost nicht: Server per `Start Spotify Remote` starten, dann `Open Spotify Remote`. Falls das automatische Oeffnen scheitert, Experimental Browser manuell auf `http://127.0.0.1:8787` setzen. Einige Kindle-Firmwares akzeptieren die `lipc` Browser-URL nicht; der Server laeuft trotzdem.

Binary startet nicht: `bin/spotify-remote-arm` muss fuer Kindle ARM gebaut und ausfuehrbar sein. Bei FAT/USB-Kopien kann das Execute-Bit fehlen; MRPI/KUAL setzt es oft nicht automatisch. Wenn moeglich, ZIP so erzeugen, dass `start.sh`, `stop.sh` und das Binary executable bleiben. Ohne Shell-Zugriff ist ein korrekt gepacktes Archiv wichtig.

## Grenzen

- Spotify Playback-Control benoetigt in der Praxis Spotify Premium.
- Der Kindle Experimental Browser ist alt; Spotify Login und Localhost-Redirect koennen je nach Firmware hakelig sein. Dafuer gibt es den manuellen Code-Fallback.
- Albumcover werden nur klein und grau angezeigt; bei Netzwerkproblemen kann die UI ohne Cover weiterlaufen.
- Ohne vorgebautes `bin/spotify-remote-arm` ist die Extension noch nicht direkt lauffaehig. Das Binary muss vor dem Kopieren per Cross-Compile erzeugt werden.
- Kein Client Secret wird verwendet, weil der Kindle-Speicher lokal unsicher ist.
