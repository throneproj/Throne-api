# Live transcript suite

`TestLive` drives a running Throne through the API and writes one JSON transcript per
subtest. It is a regression net for changes inside Throne: run it before and after a
change, on the same machine, and diff the two transcript directories.

It builds only with the `live` tag, so `go test ./...` never touches a real Throne.

> The suite starts and stops connections, switches the routing profile and, when asked,
> Tun, the system proxy and the core process. It changes the URL test address and puts it
> back, adds a group named `throne-live scratch` with two profiles and deletes it again, and
> builds backups. With `THRONE_LIVE_DESTRUCTIVE=1` it also restores a backup it just made,
> restarts Throne, and at the end shuts Throne down. It restores the connection, modes and
> route it found when it ends, best effort. Do not run it where a dropped connection
> matters.

## Subtests

| # | Subtest | What it checks |
|---|---|---|
| 01 | `status` | `GET /v1/status`: settled state, core up, version, kill switch |
| 02 | `listing` | Groups, the configured profiles by ID, their group, an unknown ID (`not_found`), routes |
| 03 | `connect` | From idle, connect `PROFILE` with wait: running and running ID; connect again (left alone); `restart` |
| 04 | `route` | Activate `ROUTE` with the profile running (restart), then the original route |
| 05 | `switch` | Connect `PROFILE2` while `PROFILE` runs |
| 06 | `contention` | Six concurrent restarts: each is ok, `busy` or `superseded`, at least one ok, and the profile runs. Then a stop right behind a start: ok or `busy` |
| 07 | `disconnect` | Disconnect with wait, then again when idle |
| 08 | `latency` | Latency test of the profiles, polled to completion; the job list shows it |
| 09 | `cancel` | Latency test of `PROFILE`'s group, cancelled; ends `cancelled` or `done` |
| 10 | `modes` | An unchanged `PUT /v1/modes`; with the opt-ins, each mode toggled and back while running |
| 11 | `core-kill` | Kill ThroneCore; the core comes back and a reconnect works |
| 12 | `capabilities` | `capabilities`, `restart_needed` and `degraded` in the status: `capabilities.tun` is a bool or `null`, the other capabilities bools; `degraded` is `null` once settled, unless the kill switch failed |
| 13 | `settings` | `GET /v1/settings`; `test_url` changed, set to the same value (nothing changed), refused changes (one bad value among good ones, a bad URL, an unknown setting) that change nothing, and put back |
| 14 | `scratch-group` | Add a plain group, import two `socks5://` links into it, an import of nothing (`nothing_imported`), delete one profile, delete the group, delete it again (`not_found`) |
| 15 | `geo` | The geo files; an unknown file (`bad_request`); with every file present, an update of the missing ones, which skips them all |
| 16 | `rule-sets` | Update while idle (`not_running`), then with `PROFILE` running |
| 17 | `backup` | Build a backup of profiles and routes, read its first 8 bytes (`THRN` and the format version), read past its end (`bad_request`), delete it, read it again (`not_found`); then download a whole backup with the library and check its magic and size |
| 18 | `restore` | Upload the archive of `backup`, apply it while incomplete (`incomplete`), read the upload's state while uploading and once checked, apply it with an unknown part (`bad_request`), then with the default parts; Throne restarts and comes back. Skipped when Throne answers `invalid_archive`: its profiles include ones, such as Extra Core profiles, that it restores only in its window |
| 19 | `restart` | `POST /v1/restart` with `PROFILE` running; Throne comes back running it if it said it would resume, else idle |
| 20 | `shutdown` | Restore the starting state, then `POST /v1/shutdown`; Throne exits |

Subtests without their settings are skipped, and their transcript says so. Each subtest
sets up its own starting state (idle, or a given profile running) without recording it,
so one failing subtest does not change the transcripts of the next. Throne answers admin
requests only on its own device unless it allows management from the local network;
elsewhere `settings`, `scratch-group`, `backup`, `restore`, `restart` and `shutdown`
record `forbidden` and are skipped.

## Running

Enable the API in Throne (**Preferences → Integration Settings → Throne API**). Start both
runs from the same state (same running profile or idle, same modes and route): the first
transcript records it.

| Variable | |
|---|---|
| `THRONE_ADDR`, `THRONE_KEY` or `THRONE_KEY_FILE` | As for thronectl |
| `THRONE_LIVE_PROFILE` | Required. ID of a regular profile (not an auto selector) that connects |
| `THRONE_LIVE_PROFILE2` | Another profile, for `switch`; `contention` then uses it |
| `THRONE_LIVE_ROUTE` | A routing profile ID, for `route` |
| `THRONE_LIVE_TRANSCRIPT_DIR` | Absolute path for the transcripts (`go test` runs in `live/`, so a relative path lands there). Default: a new directory under the system temp directory, printed at the start |
| `THRONE_LIVE_SYSTEM_PROXY=1` | Toggle the system proxy |
| `THRONE_LIVE_ALLOW_TUN=1` | Toggle Tun. Without privileges, `needs_privileges` is recorded and the toggle ends |
| `THRONE_LIVE_KILL_CORE=1` | Kill the core: `taskkill /F /IM ThroneCore.exe` on Windows, `pkill -9 -x ThroneCore` elsewhere. The test must be allowed to kill it (a root-owned core needs a root shell). Throne gives up restarting the core when it exits twice within 10 seconds, so leave at least 10 seconds between two kills |
| `THRONE_LIVE_DESTRUCTIVE=1` | Run `restore`, `restart` and `shutdown`. The restore replaces Throne's profiles, routes, OTP accounts and IP lists with the archive made minutes before, and Throne restarts twice. After `shutdown` Throne is gone: start it again yourself |

```sh
export THRONE_ADDR=127.0.0.1:9095 THRONE_KEY_FILE=~/.config/throne/api-key
export THRONE_LIVE_PROFILE=12 THRONE_LIVE_PROFILE2=15 THRONE_LIVE_ROUTE=2
export THRONE_LIVE_TRANSCRIPT_DIR=$HOME/throne-live/before
go test -tags live -run Live -v -count=1 ./...
```

```powershell
$env:THRONE_KEY_FILE = "$HOME\throne-api-key.txt"
$env:THRONE_LIVE_PROFILE = "12"; $env:THRONE_LIVE_PROFILE2 = "15"; $env:THRONE_LIVE_ROUTE = "2"
$env:THRONE_LIVE_TRANSCRIPT_DIR = "$HOME\throne-live\before"
go test -tags live -run Live -v -count=1 ./...
```

`-count=1` keeps Go from answering with a cached result, which would write no
transcripts. Tests can take minutes on a large group; add `-timeout 30m` if needed.

## Diffing two runs

Point `THRONE_LIVE_TRANSCRIPT_DIR` at a second directory (`.../after`), run again, then:

```sh
git diff --no-index ~/throne-live/before ~/throne-live/after
```

`diff -ru before after` works too. Compare runs of the same OS; Tun, system proxy and
the core differ between platforms.

Transcripts hold each request and its response with run-dependent values masked as
`"<masked>"`, keeping the key: timestamps, latencies, speeds, byte counters, country and
exit IP, job IDs (also in paths, as `/v1/tests/{job}`), group sizes and update times,
Throne's version, the sizes and SHA-256 of archives and geo files, and why a rule-set
failed to download. The IDs of the scratch group, its profiles, backups and uploads are
masked too, in paths as `/v1/groups/{group}`, `/v1/backups/{backup}` and
`/v1/restores/{upload}`. Error responses keep the status and code, not the translated
message.
Outcomes that depend on timing are recorded as checks rather than responses: which of the
concurrent restarts won, whether a cancel arrived before the test finished, and whether a
poll saw the core down. Polls are not recorded, only the state they waited for, and of an
upload only the last chunk is.
