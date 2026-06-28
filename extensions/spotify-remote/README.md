# Spotify Remote KUAL Extension

This folder is the Kindle-side extension that should be copied to:

```text
/mnt/us/extensions/spotify-remote
```

For full user-facing setup, build, install, login, troubleshooting, legal, privacy, and security information, use the repository root [README.md](../../README.md).

## Folder Contents

```text
config.xml             KUAL extension metadata
menu.json              KUAL menu entries
launch.sh              Detaches from KUAL and starts run-native.sh
run-kual.sh            Maintenance wrapper for one-shot KUAL actions
login-url.sh           Maintenance script for creating data/login_url.txt
finish-login.sh        Maintenance script for exchanging data/callback.txt
run-native.sh          Runs the native fullscreen app and restores framework on exit
stop.sh                Stops the native app
recover.sh             Emergency framework/UI recovery
nowplaying-*.sh        Passive display helpers
build.sh               Linux/macOS cross-build helper
build.ps1              Windows cross-build helper
src/native/            Native Kindle Spotify remote
data/config.example.json Public configuration template
```

## KUAL Menu

The normal KUAL menu is intentionally small:

- `Now Playing Display`: starts the Kindle now-playing display through `nowplaying-launch.sh`.
- `Create Login URL`: calls `bin/spotify-remote-arm` with `kual login` and writes `data/login_url.txt` for manual OAuth login on another device.
- `Finish Login`: calls `bin/spotify-remote-arm` with `kual finish-login` and exchanges a pasted redirect URL or code from `data/callback.txt`.

Direct control and recovery scripts remain in this folder for maintenance, but are hidden from the normal KUAL menu to keep day-to-day use simple.

The `nowplaying-*.sh` scripts are legacy passive-display helpers. They should not be wired to the normal KUAL start item because they do not run the native FBInk UI.

The native now-playing view keeps the original Kindle layout. The third track-info row shows the album by default and switches to the active Spotify context when available, for example `Playlist: <name>`, `Playlist: <id>`, or `Liked Songs`. Playlist names require the playlist read OAuth scopes; emoji-only names fall back to the Spotify playlist ID because FBInk cannot render them visibly. The lower information area also shows the active Spotify device name when Spotify provides it.

## Local Files

Runtime files are created locally on the Kindle and are ignored by Git:

```text
data/config.json
data/token.json
data/oauth.json
data/callback.txt
data/login_url.txt
data/status.txt
logs/
bin/
```

Use `data/config.example.json` as the template for local config. Never add a Spotify Client Secret.

## Build Shortcut

Windows:

```powershell
..\..\scripts\build-native.ps1
```

If `go` is not on `PATH`, set `GOEXE` first:

```powershell
$env:GOEXE='C:\path\to\go.exe'
..\..\scripts\build-native.ps1
```

Linux/macOS:

```sh
./build.sh
```

Both scripts produce `bin/spotify-remote-arm`. Release ZIPs may include the binary; source commits should not.

## Development Deploy

From the repository root on Windows:

```powershell
.\scripts\deploy-kindle.ps1
```

The deploy script copies the built binary as active `bin/spotify-remote-arm` and leaves local Kindle runtime data untouched. The KUAL login menu uses that active binary directly because it is the working form on the target Kindle.

Other Windows helper scripts live in `scripts/`:

- `build-native.ps1`: builds the Kindle ARM binary.
- `test.ps1`: validates menu JSON and runs Go tests.
- `package-kual.ps1`: creates `dist/spotify-remote-kual.zip`.
