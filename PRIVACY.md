# Privacy

This project runs locally on the user's Kindle and talks directly to Spotify's
official Web API.

## Data Stored Locally

The app may create local files under `extensions/spotify-remote/data/`:

- `config.json`: local app configuration and the user's Spotify Client ID
- `token.json`: Spotify OAuth access and refresh tokens
- `oauth.json`: temporary PKCE login state
- `callback.txt`: manual login fallback input
- `login_url.txt`: generated Spotify authorization URL
- `status.txt`: diagnostic playback status output

The app may also write logs under `extensions/spotify-remote/logs/`.

These files stay on the user's Kindle unless the user copies, backs up, shares,
or publishes them.

## Third Parties

Spotify API requests are sent to Spotify endpoints such as
`accounts.spotify.com` and `api.spotify.com`. Spotify's own privacy policy and
developer terms apply to those requests.

This project has no external server component and does not intentionally send
local tokens or logs to the project author.

## Public Repository Safety

The repository intentionally tracks only `data/config.example.json`. Real
runtime files are ignored by Git.
