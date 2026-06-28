# Kindle Spotify Remote - Internal Notes

This file holds implementation notes that should not be repeated in the public README. For user-facing setup, build, install, login, troubleshooting, legal, privacy, and security information, use the root [README.md](../README.md) plus [SECURITY.md](../SECURITY.md), [PRIVACY.md](../PRIVACY.md), and [DISCLAIMER.md](../DISCLAIMER.md).

## Documentation Ownership

- `README.md`: canonical public documentation.
- `extensions/spotify-remote/README.md`: short notes specific to the KUAL extension folder.
- `docs/PROJECT_DOCUMENTATION.md`: internal architecture, research, and maintenance notes.
- `docs/crash-logs/`: reviewed historical crash logs.

Avoid copying full setup, build, OAuth, or troubleshooting sections between these files. Link to the canonical source instead.

## Active Architecture

The primary runtime path is the native Kindle app:

```text
KUAL -> extensions/spotify-remote/menu.json
     -> launch.sh
     -> run-native.sh
     -> bin/spotify-remote-arm or bin/spotify-remote-arm.new
     -> src/native/main.go
```

The native app is responsible for:

- drawing the Kindle UI with FBInk/eips-style primitives;
- reading touch events from `/dev/input/event*`;
- reading kernel ABS min/max ranges where available;
- normalizing touch coordinates with config fallbacks;
- handling Spotify OAuth PKCE and token refresh;
- calling Spotify playback APIs;
- showing current playback context and active Spotify device names when Spotify provides them;
- showing diagnostic tap output such as `raw=x,y xy=x,y`.

The legacy browser/server implementation remains in `src/spotify-remote.go` and `www/` as a setup/development fallback. It is not the preferred daily UI.

## Kindle Runtime Rules

- Target install path is `/mnt/us/extensions/spotify-remote`.
- Keep `extensions/spotify-remote/menu.json` and `extensions/spotifyremote/menu.json` synchronized while both folders exist.
- Keep both `config.xml` files synchronized except for folder-specific IDs.
- Prefer `start framework` / `stop framework` on PW5/newer firmware; fall back to `/etc/init.d/framework` only when present.
- Always restore `preventScreenSaver` and the Kindle framework on exit or recovery.
- Keep logs short. Move only useful reviewed failures into `docs/crash-logs/`.

## Calibration Notes

Default PW5 assumptions are documented in `extensions/spotify-remote/data/config.example.json`.

The important implementation point: touch handling should not assume `0..4095` blindly. The app should prefer real kernel ABS ranges from the input device and only fall back to manual config values when kernel data is missing or wrong.

When debugging touch:

- `raw` means values read from the Linux input event stream.
- `xy` means normalized screen coordinates.
- `Tap ...` means a configured button hit was found.
- `Miss ...` means input arrived, but no button rectangle matched.

If `raw` changes but `xy` is wrong, adjust `touch_use_kernel_abs`, `touch_min_*`, `touch_max_*`, `touch_swap_xy`, or `touch_invert_*`.

If `xy` is right but labels/targets are visually offset, adjust `screen_*`, `eips_*`, `button_top`, `button_height`, or `button_gap`.

## Research Summary

External Gemini research was used as architecture input for Kindle/KUAL development. Treat these points as design guidance, not verified product claims:

- KUAL extensions should stay small and avoid unnecessary resident daemons.
- Framework stop/start must be process-detached from KUAL, hence the `launch.sh -> run-native.sh` split.
- Direct e-ink rendering is more predictable than relying on Kindle WebKit for the main UI.
- FBInk is a good future rendering layer; eips remains acceptable for simple text UI.
- Direct evdev input is reasonable, but event device number and ABS ranges vary by model/firmware.
- Avoid CGO where possible; pure Go cross-compiled with `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0` is the current target.
- `GOARM=6` is the first fallback for older devices.
- Avoid high-frequency logging or polling because Kindle storage, CPU, and battery are constrained.

Reference starting points:

- MobileRead Kindle Developer's Corner
- KindleModding KUAL/MRPI docs
- NiLuJe KUAL Booklet README
- NiLuJe FBInk
- MobileRead eips and LIPC wiki pages
- Linux kernel input event and multitouch protocol docs
- KOReader developer docs for architecture inspiration only

## Open Product TODOs

- Consider a WebLaunch/browser fallback only if native touch remains unreliable on a specific model.
- Consider moving more drawing to FBInk if cover/dashboard rendering becomes a priority.

Done:

- The native FBInk now-playing view keeps the stable main layout and uses the third track-info row for Spotify playback context when Spotify provides it. It resolves `context.href` to a display name where possible, falls back to a short Spotify URI identifier, and avoids emoji-only names that FBInk would render as an empty label.
- The native FBInk now-playing view and browser fallback show the active Spotify device name when the playback state includes `device.name`.

## Maintenance Checklist

Before changing Kindle runtime behavior:

```powershell
git status --short
.\scripts\build-native.ps1
.\scripts\test.ps1
```

## Deploy Checklist

Preferred Windows deploy:

```powershell
.\scripts\deploy-kindle.ps1
```

Use an explicit drive letter if Windows does not expose the Kindle with the `Kindle` volume label:

```powershell
.\scripts\deploy-kindle.ps1 -DriveLetter I
```

Deploy rules:

- Build locally before copying unless `-SkipBuild` is explicitly used.
- Copy the new binary to `bin/spotify-remote-arm.new`; `run-native.sh` prefers that file on the next launch.
- Preserve Kindle-local `data/config.json`, `data/token.json`, callback files, and logs.
- Keep `extensions/spotify-remote/menu.json` and `extensions/spotifyremote/menu.json` synchronized on the Kindle.
- Verify SHA256 of local binary and deployed binary.
- Eject the Kindle before launching KUAL.

Use `-DeployActiveBinary` only for a clean offline copy where the app is certainly not running; normal development deploys should keep using `.new`.

Script library:

- `scripts/lib/common.ps1`: repo paths, Go tool discovery, Kindle ARM Go environment, native build/test helpers, required-file copying.
- `scripts/lib/kindle.ps1`: Kindle USB drive detection and extension deployment.
- `scripts/build-native.ps1`: normal Windows build entry point.
- `scripts/test.ps1`: JSON validation plus Go tests.
- `scripts/package-kual.ps1`: release ZIP creation under `dist/`.
- `scripts/deploy-kindle.ps1`: USB deploy entry point built on top of the shared library.

Project convention:

- Commit each completed change separately.
- Include the AI agent in the commit body, for example `Agent: codex`.
- Update the relevant documentation whenever project changes are made, then commit and push the documentation together with the change or in the next dedicated documentation commit.
- Do not commit local `data/config.json`, `data/token.json`, logs, ZIPs, or built binaries.

## Rate Limiting

Spotify Web API calls in both runtimes pass through the central `spotifyAPI` wrapper, which detects HTTP 429 responses outside the Spotify Accounts token endpoint. The wrapper reads Spotify's `Retry-After` header, defaults to five seconds when the header is missing or invalid, stores package-level rate-limit state, and returns the typed `errRateLimited` sentinel so callers can distinguish rate limiting from other Spotify errors.

Retry handling is intentionally outside token refresh and OAuth error handling. Idle requests schedule one automatic retry after the `Retry-After` delay. The scheduled retry runs once; if Spotify returns 429 again, the state is marked non-retryable and no further retry is started by that scheduler.

The native runtime protects active playback by buffering one failed Spotify call while known playback is active. A newer 429 replaces the older pending call. After the wait, the pending call is replayed only if playback is still active; otherwise it is discarded and logged. The display countdown uses row-only eips/FBInk updates so the normal playback screen is not cleared or redrawn every second.

The browser fallback returns HTTP 429 with JSON `error`, `retry_after`, and `message` fields plus a `Retry-After` header. The web UI shows a countdown banner, disables playback controls during the wait, pauses polling so it does not retry the local server independently, and resumes after the countdown.

Test coverage lives in `extensions/spotify-remote/src/spotify-remote_test.go` and `extensions/spotify-remote/src/native/main_test.go`. The tests cover 429 interception, default retry delay, token endpoint exclusion, retry success, second-429 non-retryable state, idle versus active playback routing, replay while active, and discard after playback ends.

## AI Development Assistance

This project was developed with the following AI tools:

- **Gemini 3.1 Pro** (web): used for research on Kindle/KUAL development details, Spotify API behaviour, and ARM Linux platform specifics.
- **ChatGPT Codex (GPT-5.5 medium)**: used as the primary coding agent for implementation passes, guided by prompts written for each task.
- **Claude Sonnet 4.6**: used to write and refine the coding agent prompts that drove each implementation task.

Development workflow: each task was prompted individually, implemented by the coding agent, then committed and pushed before the next task began. Commits were made after every small completed change.
