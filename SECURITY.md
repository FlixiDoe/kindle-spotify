# Security Policy

## Supported Versions

This project is maintained as a small hobby project. The latest `main` branch
is the only supported version.

## Secrets And Local Data

Do not commit or publish:

- `extensions/spotify-remote/data/config.json`
- `extensions/spotify-remote/data/token.json`
- `extensions/spotify-remote/data/oauth.json`
- `extensions/spotify-remote/data/callback.txt`
- `extensions/spotify-remote/data/login_url.txt`
- `extensions/spotify-remote/data/status.txt`
- `extensions/spotify-remote/logs/`
- generated ZIP packages
- built binaries

`token.json` contains Spotify OAuth tokens. Treat it as private account data.

`config.json` contains the user's Spotify Client ID. A Client ID is not a
secret, but it is intentionally kept local so public users can configure their
own Spotify Developer app.

Never add a Spotify Client Secret to this project or to a Kindle install. The
Kindle storage should be treated as user-accessible, and this app is designed
to use OAuth PKCE without a Client Secret.

## Reporting A Vulnerability

Please open a GitHub issue with reproduction steps and mark clearly that it is
security-related. Do not paste real OAuth tokens, Spotify Client Secrets, or
private redirect URLs into public issues.

If you accidentally expose a Spotify Client Secret or OAuth token, revoke or
rotate it in the Spotify Developer Dashboard or by reauthorizing the app.
