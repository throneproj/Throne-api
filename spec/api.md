# Throne API, version 1

The requests Throne answers over the [wire protocol](protocol.md). Each request is a
`method` and a `path`, with an optional `query` and `body`, exactly as protocol.md describes.

The API controls the connection, Tun and system proxy modes, the routing profile and tests,
and it reads profiles and statistics. Its management requests also import profiles, add,
update and delete groups and subscriptions, delete profiles, change a chosen set of
settings, update rule-sets and geo files, back up and restore Throne's data, and restart or
shut down Throne. Most of them are [admin requests](#scope), which Throne answers only from
its own device unless it is set to allow them from the local network.

Responses never contain profile credentials, outbound configurations or subscription URLs.
Backups are the exception: they contain Throne's data, credentials included, in plain text.

## Conventions

- IDs are integers. `-1` means "none".
- Times are Unix seconds. `0` means "never" or "unknown".
- Byte counts are integers. Live rates are bytes per second; speed-test results are bits
  per second.
- Lists come wrapped in an object, such as `{"profiles": [...]}`, so fields can be added
  later.
- Every body is a JSON object.
- Unknown fields in a body and unknown query parameters are ignored.
- Binary data is standard base64 with padding (RFC 4648, section 4).
- Throne writes every request that changes something to its log, with the client address.

### Waiting

Requests that start, stop or restart the connection, and the management requests that say
so, take two optional body fields:

| Field | Type | Default | |
|---|---|---|---|
| `wait` | bool | `true` | Respond when the change has finished or failed |
| `timeout` | number | `30`, unless the request names another | Seconds to wait, 1 to 300 |

With `wait`, the response arrives once the change succeeded (200) or failed (an error). If
`timeout` passes first, the response is 202 and the change carries on in Throne. Requests
that change the connection then respond with the current [Status](#status); poll
`GET /v1/status` to follow it. The other requests name the body of their 202. With
`"wait": false` a change responds 202 at once. A request that changes nothing, such as
stopping when nothing runs, responds 200 either way.

While a profile is starting or stopping, requests that would restart it respond `busy`, and
so do the requests that delete groups or profiles, change settings, apply a restore, or
restart or shut down Throne.

### Selecting profiles

Requests that act on several profiles take one of these in the body:

| Fields | Selects |
|---|---|
| `profiles: [12, 15]` | These profiles, in this order |
| `group: 3` with optional `name` | The group's profiles in Throne's order, keeping those whose name matches `name` |

`name` is a case-insensitive regular expression (PCRE syntax) that may match anywhere in
the profile name. An invalid expression is a `bad_request`, and so is `name` together with
`profiles`. An unknown id in `profiles` is `not_found`. Repeated ids count once. Auto
selector profiles are never tested, so tests skip them.

## Scope

Requests are control requests or admin requests. Admin requests import, delete, change
settings, back up, restore, restart and shut down:

- `POST /v1/import`
- `POST /v1/groups`, `DELETE /v1/groups/{id}`, `DELETE /v1/profiles`
- `PUT /v1/settings`
- every request under `/v1/backups` and `/v1/restores`
- `POST /v1/restart`, `POST /v1/shutdown`

Every other request is a control request, among them `GET /v1/settings`, the subscription
updates and the rule-set and geo file updates. Throne answers control requests from every
client it lets in.

Throne answers admin requests only from its own device, that is from a loopback address
such as `127.0.0.1` or `::1`. With **Allow management from the local network** on, in the
Throne API settings in Throne, it answers them from every client it lets in; in
[headless Throne](#headless-throne), `--api-admin-lan` or `THRONE_API_ADMIN_LAN=1` does the
same. Otherwise an admin request gets 403 `forbidden`. The key is the same for both kinds.

After 5 connections from one address fail authentication within a minute, as those of a
client that still has an old key do, Throne refuses every connection from that address for
60 seconds: it closes them without a response. That locks out every other client at the
address too, such as those behind one NAT or those that reach a container through its
published port. Throne does not refuse an address where a client authenticated in the last
10 minutes, and it never refuses loopback addresses.

## Headless Throne

`Throne --headless` runs Throne without a window, as a service or in a container, and
`capabilities.headless` in the [Status](#status) is true. The API is how it is controlled,
so it is on from the start:

- Throne listens on `127.0.0.1:9095`, with the key in `config/api.key` in its data
  directory. Throne creates that file on its first start, readable only by the user it runs
  as, and never replaces a key in it. Only a file that holds no key at all, as after a first
  start that was cut short, gets a new key, which Throne logs. Any other unusable file keeps
  the API off, and Throne logs why: delete it to have a new key created at the next start.
  To change the key, stop Throne, delete the file, write a new one with
  `thronectl keygen -o <file>` and start Throne again, then give every client the new key
  (see [Scope](#scope) for what a client with the old one does).
- The data directory is the first of `--data-dir`, `THRONE_DATA_DIR` and systemd's
  `STATE_DIRECTORY` that is set. Without them it is `/var/lib/throne` for root on Linux,
  `/Library/Application Support/Throne` for root on macOS, `%ProgramData%\Throne` for a
  Windows service (`--service`) or a Throne that runs as LocalSystem, which Throne keeps
  for SYSTEM and Administrators only, and the user's configuration directory otherwise,
  such as `~/.config/Throne` on Linux or `%LOCALAPPDATA%\Throne` on Windows.
- When the settings of the data directory turn the API on, as a Throne with a window that
  uses the same directory may have done, Throne listens as they say, with their key.
- These flags, or the environment variables beside them, change the listener. A flag wins
  over its variable, and both win over the settings:

| Flag | Variable | |
|---|---|---|
| `--api-listen ADDRESS` | `THRONE_API_LISTEN` | `PORT` (on `127.0.0.1`), `IP:PORT`, `[IPv6]:PORT`, `*:PORT` (every address), or `off` |
| `--api-key-file FILE` | `THRONE_API_KEY_FILE` | Read the key from this file. Throne does not start when the file is missing or holds no valid key: write one with `thronectl keygen -o FILE` |
| `--api-admin-lan` | `THRONE_API_ADMIN_LAN=1` | Answer [admin requests](#scope) from the local network too |
| `--api-allow LIST` | `THRONE_API_ALLOW` | Let in only these IP addresses and subnets, such as `192.168.1.0/24`, separated by commas or spaces. Without it, a listener on a network address lets in every device that holds the key |

A variable set to an empty value counts as not set, except `THRONE_API_ALLOW`. An empty
list, or one of only commas and spaces, from `--api-allow` or `THRONE_API_ALLOW`, makes
Throne exit with status 64 rather than let in every device, and so does an entry that is
not an IP address or subnet. A compose file's `THRONE_API_ALLOW: ${VAR}` passes an empty
value when `VAR` is not set: leave `THRONE_API_ALLOW` out of the environment to let in
every device.

When the listener that `--api-listen` or `THRONE_API_LISTEN` names cannot listen, Throne
exits with status 78, or with status 1 when its address has not appeared after 30 seconds.
When the default listener, or one that the settings turn on, cannot listen, as when another
program holds its port, Throne runs on without the API (`degraded` is `api_unavailable`) and
tries again every 30 seconds. If the Throne API of another Throne answers on that port,
Throne logs so and stops trying: give one of them another port with `--api-listen`. A
listener on one address, not `*`, stays on it: when the address leaves every network
interface, no client reaches the API until it comes back, and `api` in `--health` is
`address_missing` meanwhile. To keep the API through a change of address, listen on
`*:PORT` with `--api-allow`.

`Throne --print-connection`, run as the user Throne runs as and for the same data directory,
prints the two settings that clients read, never the key itself, as `export` commands for
`eval "$(Throne --print-connection)"` in a POSIX shell. A value that the shell would split
or expand is in single quotes:

```sh
$ sudo Throne --print-connection
export THRONE_ADDR=127.0.0.1:9095
export THRONE_KEY_FILE=/var/lib/throne/config/api.key
```

It exits with status 2 when the API is off or failed to listen, as when another program
holds its address or its key is missing or invalid, or when its key is in the settings
rather than in a file. `eval` does not pass that status on, so a script that checks it runs
`conn="$(Throne --print-connection)" && eval "$conn"`. `sudo` drops these variables from the
environment, so a client run with `sudo` takes the key file as an option, such as
`sudo thronectl --key-file /var/lib/throne/config/api.key status`.

`Throne --health`, run the same way, prints one line of JSON about the running Throne. It
exits with status 0 when `ok` is true, 1 when it is not or Throne is still starting, and 2
when no Throne answers:

```sh
$ sudo Throne --health
{"api":"listening","core_restarts":0,"degraded":null,"headless_dialogs":0,"ok":true,"previous_crash":false,"ready":true,"state":"running","stuck_s":0}
```

| Field | |
|---|---|
| `ok` | Throne works: it has started, its core is up, no profile is stuck starting or stopping, and the kill switch is off or works. What a restart of Throne cannot fix leaves it true: a [kill switch](#kill-switch) that cannot work as set up or that `--no-kill-switch` turned off, and the reasons in `degraded` after `kill_switch_failed`. So it suits a readiness check; restarting Throne whenever it is false can restart it forever |
| `ready` | Throne has finished starting |
| `state`, `degraded` | As in the [Status](#status); `state` is `starting` until Throne has started |
| `stuck_s` | Seconds a profile has been starting or stopping for longer than it should, else `0` |
| `core_restarts` | How often the core process was restarted |
| `headless_dialogs` | Questions Throne skipped because it has no window. It should stay `0`; the log names them |
| `previous_crash` | The previous run of Throne did not end cleanly |
| `api` | The Throne API's listener: `off`, `listening`, `key_invalid` (its key is missing or invalid), `failed` (it could not listen, as when another program holds its port), `retrying` (its address has not appeared yet, and Throne waits up to 30 seconds for it) or `address_missing` (its address has left every network interface, so no client reaches the API until it comes back) |

They ask the running Throne through a socket that only the user Throne runs as can open:
`config/.instance` in the data directory. When that path is too long for a socket (100
bytes or more), or the data directory cannot hold one, as on FAT, exFAT or sshfs, the
socket is `headless-<hash>` in a directory of that user's own: `/run/throne` for root on
Linux, `/var/run/throne` for root on macOS, and `/tmp/throne-<uid>` otherwise. On Windows
it is a named pipe; that of a service (`--service`, or Throne run as LocalSystem) answers
Administrators and SYSTEM, so the client modes run from an elevated prompt there.

`Throne --check`, run the same way and with the flags and variables Throne starts with,
tells whether headless Throne can run here as it is set up, before its first start or when
something does not work. It changes nothing: it creates no file or directory, takes no
lock, opens the database read-only and starts nothing but the core's own check, so it can
run beside a running Throne. It prints one line per item, `[ OK ]`, `[WARN]`, `[FAIL]` or
`[INFO]`, with an indented `fix:` line below it where there is a fix, and exits with status 0
when no line is `[FAIL]` and 1 otherwise:

| Item | |
|---|---|
| build | Throne's version, Qt, the system and the user Throne runs as |
| data directory | Which one and why, as the list above picks it, and whether Throne can write it and only its user can read it. A directory that does not exist yet is `[INFO]`: Throne creates it at its first start |
| instance | Whether a Throne runs for this data directory, and its PID |
| settings | Read from `config/throne.db`; Throne's defaults when there is none yet |
| API | Where the API listens, from the settings, flags and variables |
| TLS | Whether Throne can make TLS connections, which subscriptions and downloads need |
| core | Whether `ThroneCore` is there and runs, and what it reports about its privileges |
| Tun | Whether the core can run Tun: on Linux root or the capabilities Tun needs, CAP_NET_ADMIN and `/dev/net/tun`; on macOS a core that runs as root. `[WARN]` with how to grant it, such as `setcap` for the core, or `--cap-add NET_ADMIN --device /dev/net/tun` for a container |
| kill switch | Whether the core can run the [kill switch](#kill-switch): root, and nftables on Linux. `[WARN]` otherwise |
| ports | Whether the ports Throne listens on are free: the API, the mixed inbound unless its port is random or it is off, the DNS port (`dns_in_port`), and the Clash API (`clash_api_port`) and the sing-box API (`singbox_api_port`) when they are on. Ports that the running Throne of this data directory holds are `[ OK ]`. The `fix:` lines name the [setting](#settings) to change, such as `thronectl settings set dns_in_port=PORT` |
| host | Another headless Throne on this computer, and whether something else holds Tun here (see [One Tun per computer](#one-tun-per-computer)). The Tun of the running Throne of this data directory is `[INFO]`, also when its profile runs Tun after Tun mode was turned off or comes from a custom configuration |
| supervisor | Whether systemd started Throne, with a notify socket and a watchdog, and what `--restart-mode auto` does then (see [systemd](#systemd)); on Windows, what a restart does under the service control manager (`--service`) and outside it |
| device ID | On Linux, where the device ID that Throne can send with subscription requests comes from: `/etc/machine-id`, or on a system without one, such as a container, `config/hwid`, which Throne creates at its first start |

Only what keeps Throne from working at all is `[FAIL]`: an unusable data directory, no TLS, a
core that is missing or does not run, or a port that another program holds. Without the
privileges for Tun or the kill switch the lines are `[WARN]`, so a Throne that only serves its
proxy inbound passes.

Headless Throne asks nothing. What a Throne with a window asks in a dialog, it decides on
its own or writes to its log:

- Until it has started, every request gets 503 `starting`; try again a moment later.
- It never sets the system proxy: `PUT /v1/modes` with `"system_proxy": true` gets 409
  `system_proxy_unsupported`.
- It cannot ask for the privileges Tun needs, so turning Tun on gets `needs_privileges`
  unless it runs with them, as root for example. On Linux and macOS that is also the answer
  while Throne does not know yet whether it has them (`capabilities.tun` is `null`).
  `Throne --check` names what Tun lacks and how to grant it. One Throne at a time can run
  Tun on a computer: see [One Tun per computer](#one-tun-per-computer).
- When it starts, it starts the profile that ran when it last stopped, whatever
  `remember_enable` says, unless it was started with `--no-resume` or `THRONE_RESUME=0`.
  Tun mode comes back as it was. If Tun cannot come back, Throne does not start the profile
  without it: `degraded` is `tun_unavailable`.
- When `kill_switch` is on, the [kill switch](#kill-switch) comes back at every start,
  unless Throne was started with `--no-kill-switch` or `THRONE_KILL_SWITCH=0`. Then it
  stays off for that run and fails closed with the code `overridden`: `degraded` is
  `kill_switch_failed`, and profiles are refused with `kill_switch_inactive` until
  `kill_switch` is turned off. The setting stays as it was saved.
- [`POST /v1/restart`](#post-v1restart) and the restart after a
  [restore](#post-v1restoresidapply) make it exit with status 75, for its service manager
  to start it again, or start it again on its own (in the same process; on Windows as a
  new process), as `--restart-mode` says.
  [`POST /v1/shutdown`](#post-v1shutdown) makes it exit with status 0. Either way the
  connection drops: until Throne listens again, connections fail, and then requests get
  `starting` until it has started.

Besides `core_unavailable` and `kill_switch_failed`, headless Throne reports more reasons
in `degraded`. Throne reports the first that applies, in this order; a client should take
a value it does not know as degraded too:

| `degraded` | |
|---|---|
| `core_gave_up` | The core process kept failing and Throne gave up on it. Throne exits with status 70, for its service manager to start it again. When none of the three starts of Throne before this one had a core that stayed up for a minute, Throne stays up instead, so the API stays reachable, and tries the core again every 5 minutes |
| `core_unavailable` | The core process is not up |
| `kill_switch_failed` | The kill switch is on and failed; see [Kill switch](#kill-switch) |
| `api_unavailable` | The Throne API is on but does not listen, as when another program holds its port or its key is unusable. A client cannot reach the API to see this: `Throne --health` reports it, with the listener's state in `api`. `ok` stays true |
| `tun_unavailable` | Tun mode was on when Throne last stopped, and Throne cannot turn it on now, since it lacks the privileges Tun needs or cannot tell whether it has them, or something else on this computer holds Tun (`tun_conflict` in the Status says what). It did not start the profile without Tun. `PUT /v1/modes` with `"tun": false` turns Tun mode off and clears this; then start the profile with `PUT /v1/connection` |
| `resume_suppressed` | Throne did not start the profile it would resume, because each of the last three times it did, Throne or its core stopped unexpectedly within a minute. Start the profile through the API: once it has run for a minute, this clears |
| `vpn_auth_failed` | The server of a VPN profile, such as OpenVPN or OpenConnect, rejected its credentials, and Throne cannot ask for others. It clears when the profile stops |
| `vpn_challenge_pending` | The server of a VPN profile asks for an answer, such as a one-time code, that Throne cannot give on its own. Throne cancels the question when it expires, after 5 minutes at most |

### One Tun per computer

Tun's interface, Throne's DNS port and the routes that Tun sets belong to the whole
computer, so one Throne at a time can run Tun on it. Headless Throne turns Tun on only when
nothing else holds them: no `throne-tun` interface of another Throne (Linux and Windows), no
interface that holds the address of its Tun (`tun_ipv4_cidr`), and, while no profile
runs, its DNS port (`dns_in_port`, `5533` by default) free on `127.0.0.1`. Otherwise
`PUT /v1/modes` with `"tun": true` gets 409 `tun_conflict`, whose message says what holds
Tun, `tun_conflict` in the [Status](#status) says the same, and Tun mode that cannot come
back at the start leaves `degraded` at `tun_unavailable`. When a network of the computer,
such as a Docker bridge, uses the address of the Tun, give Tun a range that no network
there uses, such as with `thronectl settings set tun_ipv4_cidr=172.29.0.1/24`; when another
program holds the DNS port, give Throne another one with `dns_in_port`.

Headless Throne also marks that it runs, for the other Thrones of the computer: on Linux
and macOS in `/run/throne` (`/var/run/throne` on macOS) when it runs as root, else in
`$XDG_RUNTIME_DIR/throne`; on Windows as the named mapping `Global\ThroneHeadless`, or
`Local\ThroneHeadless` when it is not elevated and does not run as a service. A Throne with
a window sees a headless Throne that runs as root or as its own user (on Windows: one in
`Global\`, or one in `Local\` in its own Windows session). When it starts while one runs,
it warns in its log and in a notice, and as long as one runs, whether that one uses Tun or
not, it refuses Tun with the same `tun_conflict`: stop the headless Throne, or control it
with thronectl. It also does not update itself while the headless Throne runs from the same
installation. A second headless Throne, with another data directory, still runs, and the
interface and port checks above keep the two from running Tun at once.

### Exit statuses

| Status | |
|---|---|
| `0` | Stopped: SIGTERM, SIGINT or [`POST /v1/shutdown`](#post-v1shutdown); on Windows also a service stop, Ctrl+C, closing the console, signing out or `taskkill` without `/F` |
| `1` | Throne could not start, for a reason that may pass: the lock or the instance socket of its data directory, a database error, its core's IPC listener, or an `--api-listen` address that did not appear within 30 seconds |
| `3` | Another Throne uses the data directory |
| `64` | A bad option, option value or environment variable |
| `70` | The core kept failing and Throne gave up on it, or the core has not been usable for 60 seconds (see [Watchdog](#watchdog)); not after three starts in a row without a working core (see `core_gave_up` above) |
| `71` | A profile has been starting for 180 seconds, or stopping for 90 (see [Watchdog](#watchdog)); not after three starts in a row without a working core |
| `75` | A restart that Throne leaves to its service manager: [`POST /v1/restart`](#post-v1restart), a [restore](#post-v1restoresidapply) or SIGHUP, as `--restart-mode` says |
| `78` | A configuration that a restart cannot fix: the data directory, the file `--api-key-file` names, the `--api-listen` address, or an unusable database file. For the database, the log says what to do: move a damaged `config/throne.db` aside together with its `-wal` and `-shm` files, or put back a copy of them; when they are read-only, make them writable by the user Throne runs as |

A service manager should start Throne again after 1, 70, 71 and 75, and not after 64 or 78,
which need a change first. `Throne --check` exits with 0 or 1 as described above, and with
64 for a bad option.

### Watchdog

Headless Throne watches its own progress, with or without a service manager:

- When a profile has been starting for 180 seconds, or stopping for 90, Throne exits with
  status 71.
- When its core has not been usable for 60 seconds, as when it hangs or stops answering
  Throne, while Throne has not given up on it, Throne exits with status 70. One restart of
  the core after a crash takes less than that.

Throne logs `EXIT <status>: <reason>` before it exits, and its service manager starts it
again. A profile that Throne was resuming then counts as one that stopped early, so after
three such starts in a row it is not resumed (`resume_suppressed`). After three starts in a
row without a working core, Throne does not exit: it logs
`[Watchdog] <reason>; staying up (stay-up mode), resetting the core` once and restarts its
core instead, so the API stays reachable.

### systemd

Run headless Throne as a `Type=notify` service. Throne then tells systemd how it is doing,
from its main process, as the default `NotifyAccess=main` expects:

- `READY=1` once it has started, as `ready` in `Throne --health` says, and also when it
  stays up after three starts in a row without a working core. Until then it extends the
  start timeout by 60 seconds every 20 seconds, and by 120 seconds before it opens its
  database, which can take a while after an update. So `TimeoutStartSec` ends only a start
  that hangs.
- `STATUS=`, the line `systemctl status` shows: `Starting`, `Starting core`,
  `Core restarting 2/5`, `Idle`, `Connecting <profile>`, `Connected <profile>`, with
  ` (Tun)` in Tun mode, `Stopping <profile>`, `Degraded: <reason>` with a `degraded`
  value, and `Stopping` while Throne exits.
- `STOPPING=1` when it begins to exit, whatever the reason.
- `WATCHDOG=1` with `WatchdogSec` set: every third of it, but at most once a second, as long
  as Throne's main loop runs. When the loop hangs and `WatchdogSec` passes without one,
  systemd kills Throne with SIGABRT, and `Restart=on-failure` starts it again.

Throne does not pass systemd's variables, such as `NOTIFY_SOCKET` and
`CREDENTIALS_DIRECTORY`, or the `THRONE_` variables it reads, such as `THRONE_API_KEY_FILE`,
on to its core and the programs the core starts. A unit such as:

```ini
[Unit]
Description=Throne
Wants=network-online.target
After=network-online.target

[Service]
Type=notify
ExecStart=/opt/throne/Throne --headless
StateDirectory=throne
WatchdogSec=60
Restart=on-failure
RestartPreventExitStatus=64 78
KillMode=mixed

[Install]
WantedBy=multi-user.target
```

keeps its data in `/var/lib/throne` (`StateDirectory`). `KillMode=mixed` sends SIGTERM to
Throne alone, so Throne stops its core and the kill switch itself, in order; systemd kills
what is left once Throne has exited, or when `TimeoutStopSec` passes. Under systemd,
`--restart-mode auto` exits with status 75 for a restart, and `Restart=on-failure` starts
Throne again.

### Recovery

If saved settings keep Throne unreachable, such as a kill switch or Tun, start it once with
`--no-resume --no-kill-switch`, or with `THRONE_RESUME=0` and `THRONE_KILL_SWITCH=0` in its
environment. No profile starts and the kill switch stays off for that run, so thronectl on
the same device can change the settings, such as with
`thronectl settings set kill_switch=false` or `thronectl tun off`. Then start Throne as
usual. Until `kill_switch` is off, profiles stay refused in that run, so none runs without
the kill switch.

After three starts in a row without a working core, Throne stays up instead of exiting
with status 70 or 71 (`degraded` is `core_gave_up` when its core gave up; see also
[Watchdog](#watchdog)), so the API stays reachable for such a fix even without these flags.
An unusable `config/api.key` keeps the API off: delete it, and Throne creates a new key at
its next start.

## Objects

### Profile

```json
{
  "id": 12,
  "group": 3,
  "name": "DE Frankfurt",
  "type": "vless",
  "address": "de1.example.com:443",
  "latency_ms": 85,
  "latency_at": 1760000000,
  "download_bps": 48300000,
  "upload_bps": 9100000,
  "country": "DE",
  "ip_out": "203.0.113.7",
  "traffic_up": 1048576,
  "traffic_down": 73400320,
  "running": false
}
```

| Field | |
|---|---|
| `name` | The name Throne shows |
| `type` | Throne's profile type, such as `vless`, `shadowsocks`, `chain`, `autoselector` |
| `address` | The address Throne shows. For an auto selector, the name of the group it tracks |
| `latency_ms` | Last URL test: above 0 is the latency in ms, `0` not tested, `-1` failed, `-2` the VPN tunnel came up but the test request failed |
| `latency_at` | When `latency_ms` was measured |
| `download_bps`, `upload_bps` | Last speed test in bits per second: `0` not tested, `-1` failed |
| `country` | Country code (ISO 3166-1 alpha-2) from the last test that reported one, or `""` |
| `ip_out` | Exit IP address from the last IP test, or `""` |
| `traffic_up`, `traffic_down` | Bytes sent and received through this profile, as Throne shows them |
| `running` | Whether this is the profile Throne is running |

### Group

```json
{"id": 3, "name": "Provider", "subscription": true, "archived": false, "profile_count": 120, "updated_at": 1760000000}
```

`updated_at` is the last subscription update, `0` for a group without a subscription.

### Route

```json
{"id": 1, "name": "Default", "active": true}
```

### Status

```json
{
  "state": "running",
  "profile": {"id": 12, "name": "DE Frankfurt", "...": "a Profile object"},
  "last_profile_id": 12,
  "tun": true,
  "tun_conflict": null,
  "system_proxy": false,
  "route": {"id": 1, "name": "Default", "active": true},
  "kill_switch": "off",
  "core": true,
  "inbound": {"enabled": true, "address": "127.0.0.1", "port": 2080},
  "version": "1.4.0",
  "capabilities": {"tun": true, "kill_switch": true, "headless": false},
  "restart_needed": false,
  "degraded": null
}
```

| Field | |
|---|---|
| `state` | `idle`, `connecting`, `running` or `stopping` |
| `profile` | The running profile, or `null` |
| `last_profile_id` | The profile `{"select": "last"}` would start, or `-1` |
| `tun`, `system_proxy` | Whether the mode is on. The modes apply while a profile runs |
| `tun_conflict` | `null`, or why Tun cannot be turned on because something else on this computer holds it, such as another Throne (see [One Tun per computer](#one-tun-per-computer)); turning Tun on meanwhile gets `tun_conflict`. `null` while this Throne runs Tun itself. The text is for people and may be translated |
| `route` | The active routing profile, or `null` if it no longer exists |
| `kill_switch` | `off`, `arming`, `armed` or `failed` |
| `core` | Whether Throne's core process is up |
| `inbound` | Throne's mixed (SOCKS and HTTP) proxy: the configured listen address and port. `enabled` is false when it is turned off |
| `version` | Throne's version |
| `capabilities` | What this Throne can do, below |
| `restart_needed` | Changes, such as settings, wait for the running profile to restart. `PUT /v1/connection` with `restart` applies them |
| `degraded` | `null`, or why Throne cannot work as set up: `core_unavailable` (the core process is not up) or `kill_switch_failed` (the kill switch is on and failed). [Headless Throne](#headless-throne) reports more reasons |

| Capability | |
|---|---|
| `tun` | Throne can run Tun, as far as it knows without asking, or is set not to ask for privileges. On Linux and macOS the core checks its own privileges each time it starts: on Linux it needs to run as root or with the capabilities Tun needs, with CAP_NET_ADMIN in effect, and to open `/dev/net/tun`, so a root container without `NET_ADMIN` or the tun device gets `false`; on macOS it needs to run as root (a setuid-root core). On Windows Throne needs to be elevated. `null` while Throne does not know yet: on Linux and macOS, before the core connects or while its check runs. `Throne --check` names what is missing |
| `kill_switch` | The kill switch works here: it needs a core that runs as root with CAP_NET_ADMIN on Linux, one that runs as root on macOS, and an elevated Throne on Windows. Until the core has answered its check, this only says whether the system supports the kill switch |
| `headless` | Throne runs without a window (`Throne --headless`) and asks nothing; see [Headless Throne](#headless-throne) |

### Job

```json
{
  "id": 7,
  "kind": "latency",
  "state": "done",
  "profile_ids": [12, 15, 16],
  "started_at": 1760000000,
  "finished_at": 1760000042,
  "results": [{"id": 12, "latency_ms": 85, "...": "Profile objects"}]
}
```

| Field | |
|---|---|
| `kind` | `latency`, `ip` or `speed` |
| `state` | `running`, `done` or `cancelled` |
| `profile_ids` | The profiles being tested, in test order |
| `results` | The tested profiles as they are after the test. Only present once `state` is not `running` |

### Update result

The outcome of a subscription update:

```json
{"ok": true, "added": 3, "updated": 10, "removed": 1, "kept": 0}
{"ok": false, "error": "..."}
```

| Field | |
|---|---|
| `added`, `updated`, `removed` | Profiles the update added, changed and removed |
| `kept` | Profiles the subscription no longer lists that Throne kept because they are in use, such as the running profile |
| `error` | Why the update failed, when `ok` is false |

## Status

### `GET /v1/status`

Returns the [Status](#status).

## Groups and profiles

### `GET /v1/groups`

Returns `{"groups": [Group...]}` in Throne's tab order.

### `GET /v1/profiles`

Query parameters, all optional and combined:

| Parameter | |
|---|---|
| `group` | Only this group's profiles, in Throne's order |
| `name` | Only names matching this regular expression (see [Selecting profiles](#selecting-profiles)) |
| `ids` | Only these profiles, comma-separated: `ids=12,15` |

Returns `{"profiles": [Profile...]}`. Without `group` or `ids` it returns every profile of
every group, group by group in tab order. An unknown `group` is `not_found`.

### `GET /v1/profiles/{id}`

Returns one [Profile](#profile).

### `POST /v1/import`

Admin. Imports profiles into a group.

```json
{"as": "profiles", "text": "vless://...\nss://...", "group": 3}
{"as": "url", "url": "https://example.com/list.txt", "group": 3}
```

| Field | |
|---|---|
| `as` | Required. `profiles` imports `text`; `url` downloads `url` once and imports what it returns |
| `text` | Share links, a subscription body, a Clash, sing-box or Xray configuration, or a WireGuard or OpenVPN file. A line that holds only an http or https URL is an HTTP proxy profile |
| `url` | An http or https URL |
| `group` | The group to import into. Default: the group Throne shows when the request arrives |

The request takes the [waiting](#waiting) fields, with a default `timeout` of 60. If it
passes first, the response is 202 with `{"group": 3, "state": "queued"}` and the import
carries on.

Returns:

```json
{
  "group": 3,
  "imported": 12,
  "profile_ids": [40, 41, 42],
  "failed": 1,
  "warned": 2,
  "issues": [
    {"severity": "error", "field": "port", "message": "...", "count": 1, "examples": ["vless://host:70000 #name"]}
  ],
  "errors": []
}
```

| Field | |
|---|---|
| `imported`, `profile_ids` | How many profiles were added, and their ids |
| `failed` | Entries that could not be imported |
| `warned` | Entries imported with a warning |
| `issues` | The problems of the entries, alike ones together: `severity` (`error`, not imported, or `warning`), the `field` it concerns, the `message`, the `count` of entries, and a few `examples`. Examples never contain credentials |
| `errors` | Why the whole document failed, such as a configuration that does not parse |

Throne logs every import once: one line for an import without problems, else its problems.
It never shows a report window for the API.

Errors: `bad_request` (no `as` or an unknown one, an empty `text` or `url`, a `url` that is
not http or https), `not_found` (unknown group), `group_archived`, `subscription_group` (the
group's next subscription update would remove the profiles), `nothing_imported` (the
message gives the summary and the first problems), `fetch_failed` (`url` could not be
downloaded).

### `POST /v1/groups`

Admin. Adds a group at the end of the tab order.

```json
{"name": "Provider", "url": "https://sub.example/...", "auto_update": true, "update": true}
```

| Field | |
|---|---|
| `name` | Required, not empty |
| `url` | Makes a subscription group that downloads this http or https URL. Without it the group is a plain one |
| `auto_update` | Default `true`: the subscription takes part in Throne's automatic updates |
| `update` | Default `true`: download the subscription now |

The first update takes the [waiting](#waiting) fields, with a default `timeout` of 60. If
it passes first, the response is 202 with `{"group": Group}` and the update carries on.

Returns `{"group": Group, "update": Update result}`. `update` is `null` when no update ran.
A failed first update still returns 200, since the group exists, with `"ok": false`.

Errors: `bad_request`.

### `POST /v1/groups/{id}/update`

Updates a subscription group. It takes the [waiting](#waiting) fields, with a default
`timeout` of 60. If it passes first, the response is 202 with `{"group": Group}` and the
update carries on. An update of a group that is already updating waits for that update.

Returns `{"group": Group, "update": Update result}`.

Errors: `not_found`, `not_subscription`, `group_archived`, and for a failed update
`fetch_failed` or `nothing_imported`.

### `POST /v1/groups/update`

Updates every subscription group that is not archived, in tab order. It takes the
[waiting](#waiting) fields, with a default `timeout` of 120. If it passes first, the
response is 202 with `{"queued": [3, 4]}` and the updates carry on.

```json
{"results": [{"id": 3, "ok": true, "added": 3, "updated": 10, "removed": 1, "kept": 0}, {"id": 4, "ok": false, "error": "..."}]}
```

Returns an [update result](#update-result) with the group `id` for each group. A failed
update is a result, not an error. Without a subscription group the response is
`{"results": []}`.

### `DELETE /v1/groups/{id}`

Admin. Deletes the group and its profiles. If the running profile belongs to the group,
Throne stops it first. The request takes the [waiting](#waiting) fields, with a default
`timeout` of 30. If it passes during the stop, the response is 202 with
`{"deleted": 3, "state": "stopping"}`, and Throne deletes the group once the profile has
stopped.

Returns `{"deleted": 3}`, the group's id. A running chain or routing profile that uses one
of the group's profiles keeps running, and `restart_needed` turns true in the
[Status](#status).

Errors: `not_found`, `last_group` (it is the only group), `busy` (its subscription is
updating, or a profile is starting or stopping), `editing` (the group or one of its
profiles is open in an editor in Throne), `internal` (Throne could not delete the group,
which stays as it was).

### `DELETE /v1/profiles`

Admin. Deletes the [selected profiles](#selecting-profiles). The body also takes the
[waiting](#waiting) fields, with a default `timeout` of 30.

If the running profile is among them, Throne stops it first, as `DELETE /v1/groups/{id}`
does; the 202 is `{"deleted": 5, "state": "stopping"}`. A running chain or routing profile
that uses one of them keeps running, and `restart_needed` turns true in the
[Status](#status).

Returns `{"deleted": 5}`, the number of profiles. If some of them could not be deleted,
the response is `internal`, and the others are deleted all the same.

Errors: `bad_request`, `not_found`, `no_profiles`, `busy`, `editing`, `internal`.

## Connection

### `PUT /v1/connection`

Starts a profile. The body names it in one of three ways, plus the [waiting](#waiting)
fields:

```json
{"profile": 12}
{"select": "last"}
{"select": "lowest_latency", "group": 3, "name": "DE|NL", "max_age": 3600}
```

| Field | |
|---|---|
| `profile` | Start this profile |
| `select: "last"` | Start the profile Throne would resume: the running one, else the one that ran last, else the remembered one |
| `select: "lowest_latency"` | Start the profile with the lowest latency, as described below |
| `restart` | `true` restarts the profile even if it is already running. Default `false`: an already running profile is left alone and the response is 200 at once |

`lowest_latency` takes the profiles of `group` (default: the group Throne currently shows)
whose name matches `name`, leaving out auto selectors. An earlier result counts only if it
is younger than `max_age` seconds (default `3600`; `0` means no earlier result counts).
Throne first runs a URL test on every candidate without a counting result, then starts the
candidate with the lowest `latency_ms` above 0; results from that test always count. On a
tie the earlier profile in the group wins. The test is part of the wait: if `timeout`
passes during the test, the response is 202 and Throne still finishes the test and starts
the winner.

Returns the [Status](#status) once the profile runs.

Errors: `bad_request` (both `profile` and `select`, neither, or an unknown `select`),
`not_found`, `no_profiles`, `no_working_profile`, `busy`, `superseded`, `group_archived`,
`kill_switch_inactive`, `kill_switch_blocked`, `build_failed`, `geo_assets_missing`,
`strict_route_unavailable`, `tun_failed`, `start_failed`, `core_unavailable`, `exiting`.

### `DELETE /v1/connection`

Stops the running profile. The body is optional and takes the [waiting](#waiting) fields.
Returns the [Status](#status). Stopping when nothing runs is not an error; the response is
the current Status. While a profile is starting or stopping the response is `busy`.

## Modes and routing

### `PUT /v1/modes`

```json
{"tun": true, "system_proxy": false}
```

Both fields are optional; a mode that is left out stays as it is. Throne remembers the
change the same way it remembers the toggles in its window. Turning Tun on or off restarts
a running profile, so this request takes the [waiting](#waiting) fields.

Returns the [Status](#status).

When [headless Throne](#headless-throne) could not bring Tun mode back at its start
(`degraded` is `tun_unavailable`, `tun` is false), `"tun": false` turns Tun mode off for
good and clears that. It starts no profile; `PUT /v1/connection` does.

Errors: `needs_privileges` (Tun needs privileges that Throne does not have; grant them in
Throne once, or start headless Throne with them), `tun_conflict` (something else on this
computer holds Tun, such as another Throne; the message and `tun_conflict` in the
[Status](#status) say what, see [One Tun per computer](#one-tun-per-computer)),
`mixed_inbound_disabled` (the system proxy needs Throne's mixed inbound),
`system_proxy_unsupported` (headless Throne never sets the system proxy), plus the errors of
`PUT /v1/connection` for the restart.

### `GET /v1/routes`

Returns `{"routes": [Route...]}`.

### `PUT /v1/routes/active`

```json
{"id": 2}
```

Makes this routing profile active and restarts a running profile so it takes effect, so
it takes the [waiting](#waiting) fields. If the routing profile is already active nothing
restarts. Returns the [Status](#status).

Errors: `not_found`, `editing` (the routing profiles are open in an editor in Throne), plus
the errors of `PUT /v1/connection` for the restart.

## Tests

Throne runs one test at a time, whether the API or its window started it.

### `POST /v1/tests`

```json
{"kind": "latency", "group": 3, "name": "DE|NL"}
```

| Field | |
|---|---|
| `kind` | `latency` (Throne's URL test), `ip` (exit IP test) or `speed` (speed test, in the mode set in Throne) |
| selection | See [Selecting profiles](#selecting-profiles) |
| `wait` | Default `false`. With `true`, respond when the test finishes |
| `timeout` | Seconds to wait when `wait` is true: 1 to 300, default 300 |

Returns the [Job](#job): 202 while it runs, 200 once it is finished.

Errors: `bad_request`, `not_found` (unknown group), `no_profiles` (the selection is empty),
`busy` (a test is already running).

### `GET /v1/tests`

Returns `{"jobs": [Job...]}`, newest first, without `results`. Throne keeps the last 32
jobs until it restarts.

### `GET /v1/tests/{id}`

Returns the [Job](#job) with status 200, also while it runs: follow `state`.

### `DELETE /v1/tests/{id}`

Stops the job if it is running and responds once it has stopped, with the Job in state
`cancelled`. If it has not stopped after 10 seconds, the response is 202 with the Job
still `running`. A finished job is returned unchanged.

### `DELETE /v1/results`

Clears the test results (latency, speed, country and exit IP) of the
[selected profiles](#selecting-profiles). Returns `{"cleared": 3}`.

## Statistics

### `GET /v1/stats/live`

```json
{"proxy": {"up": 1200, "down": 56000}, "direct": {"up": 0, "down": 310}}
```

Traffic through the proxy and direct traffic, in bytes per second over the last second.
All zero when no profile runs.

### `GET /v1/stats/traffic`

| Parameter | |
|---|---|
| `by` | `profile` (default) or `app` |
| `from`, `to` | Unix seconds. Default: the last 24 hours |

```json
{
  "by": "profile",
  "from": 1759913600,
  "to": 1760000000,
  "items": [
    {"id": 12, "name": "DE Frankfurt", "up": 10485760, "down": 734003200},
    {"id": -101, "name": "Direct", "up": 2048, "down": 409600}
  ]
}
```

Traffic totals from Throne's traffic statistics, largest total first. With `by=app`, items
have no `id`, and `name` is the process name. Direct traffic is profile `-101`; traffic
from speed tests is the app `Speedtest`.

Errors: `bad_request` (`by` is neither `profile` nor `app`, `from` or `to` is not an integer, or
`from` is after `to`), `stats_disabled` when traffic statistics are turned off in Throne.

## Settings

### `GET /v1/settings`

Returns `{"settings": {"log_level": "info", ...}}` with the settings in the table below. A
setting that this platform lacks is absent. Values are returned as stored, even invalid
ones.

### `PUT /v1/settings`

Admin. Changes settings.

```json
{"settings": {"log_level": "debug", "kill_switch": true}}
```

Throne checks every setting first and changes nothing when one is wrong: the
`bad_request` names the setting. An unknown setting, or one this platform lacks, is a
`bad_request`. A setting given its current value, as `GET` returns it, is left alone:
Throne neither checks nor changes it, even when the stored value is invalid. So a client
can send back the settings of a `GET` with some of them changed. A `null` value leaves the
setting as it is.

Returns `{"settings": {...}, "changed": ["log_level"], "restart_needed": true}`: every
setting after the change, the names of those that changed, sorted, and `restart_needed`
as in the [Status](#status): whether this change, or an earlier one, waits for the running
profile to restart (the last column below).

Errors: `bad_request`, `busy` (a profile is starting or stopping, or a test is running),
`editing` (a settings window is open in Throne), `mixed_inbound_disabled`
(`inbound_enabled` false while the system proxy is on), `exiting`.

| Setting | Type | | Restart |
|---|---|---|---|
| `remember_enable` | bool | Remember the running profile and start it again when Throne starts. Headless Throne does that either way, unless it was started with `--no-resume` | |
| `log_level` | string | Core log level: `trace`, `debug`, `info`, `warn`, `error`, `fatal` or `panic` | profile |
| `xray_log_level` | string | Xray log level: `debug`, `info`, `warning`, `error` or `none` | profile |
| `inbound_enabled` | bool | Throne's mixed inbound is on. `false` is refused while the system proxy is on | profile |
| `inbound_address` | string | Listen address of the mixed inbound: an IP address | profile |
| `inbound_port` | number | Port of the mixed inbound: 1 to 65535, and none of Throne's other ports. Changing it turns off the random port | profile |
| `test_url` | string | URL of the URL test: http or https | |
| `test_concurrency` | number | Profiles tested at once: 1 to 499 | |
| `test_timeout_ms` | number | URL test timeout in milliseconds: 0 to 60000, `0` the core's default | |
| `sub_auto_update` | number | Minutes between automatic subscription updates: 30 to 525600, `0` off | |
| `sub_respect_server_interval` | bool | Use the update interval a subscription asks for | |
| `route_auto_update` | number | Minutes between automatic updates of remote routing profiles: 30 to 525600, `0` off | |
| `kill_switch` | bool | The [kill switch](#kill-switch) | |
| `tun_strict_route` | bool | Tun strict routing | tun |
| `tun_auto_redirect` | bool | Tun auto redirect. Linux only | tun |
| `tun_mtu` | number | Tun MTU: 1000 to 10000 | tun |
| `tun_ipv6` | bool | Tun takes IPv6 traffic | tun |
| `tun_ipv4_cidr` | string | Tun's IPv4 address with its prefix length, such as `172.19.0.1/24`: a range that no network of the computer uses (see [One Tun per computer](#one-tun-per-computer)) | tun |
| `tun_ipv6_cidr` | string | Tun's IPv6 address with its prefix length, such as `fdfe:dcba:9876::1/96`, used while `tun_ipv6` is true | tun |
| `dns_in_port` | number | Port of the core's DNS server on `127.0.0.1`, TCP and UDP: 1 to 65535, and none of Throne's other ports | profile |
| `clash_api_port` | number | Port of the core's Clash API: 1 to 65535 and none of Throne's other ports, `0` off | profile |
| `clash_listen_addr` | string | Listen address of the Clash API: an IP address or host name, or `""` for `127.0.0.1`. Its secret is set only in Throne's settings window; without one, an address that is not loopback is refused, since anyone who reaches the Clash API controls the core | profile |
| `singbox_api_port` | number | Port of the core's sing-box API on `127.0.0.1`: 1 to 65535 and none of Throne's other ports, `0` off | profile |
| `remote_dns` | string | DNS server for proxied traffic, such as `https://8.8.8.8/dns-query` | profile |
| `direct_dns` | string | DNS server for direct traffic, such as `localhost` | profile |
| `ntp_enabled` | bool | The core keeps its time with NTP. Turning it on with an empty `ntp_interval` sets that to `30m` | profile |
| `ntp_server` | string | NTP server host, or `""` for the core's default | profile |
| `ntp_interval` | string | How often NTP runs: a duration such as `30m` or `1h` | profile |

Restart `profile`: the change takes effect when the running profile restarts. `tun`: the
same, when Tun is on. Empty: at once. Turning an automatic update, the Clash API or the sing-box
API off keeps its interval or port for later; `GET` reads it as `0`.

### Kill switch

While `kill_switch` is on, Throne blocks the traffic of its device that does not go through
Throne, also while no profile runs, except loopback and, unless Throne is set otherwise, the
local network. An API that listens on loopback only needs nothing more, since loopback is
always allowed. When the Throne API listens on another address, the kill switch also lets
out the replies to new connections that clients open to the API's port, on Linux, macOS and
Windows, so a client can connect wherever it connects from. On macOS, API connections that
are already open when the kill switch arms, as when it is turned on, are cut once, and
their clients connect again; when it arms again while it is `armed`, as after a change to a
setting it depends on, open connections stay.

This exception goes by the port alone: another program that listens on the same port at
another address of the device gets its replies through too. Programs on other ports get no
exception: an SSH session from outside the local network ends when the kill switch arms.

When the kill switch is on but cannot work, `kill_switch` is `failed` in the
[Status](#status) and `degraded` is `kill_switch_failed`. Throne then refuses to start
profiles with `kill_switch_inactive`, whose message says why. Turning `kill_switch` off ends
this. Headless Throne logs the cause with a code, such as
`[Kill switch] Not active (privilege): ...`:

| Code | |
|---|---|
| `privilege` | Throne lacks the privileges the kill switch needs: root on Linux and macOS, a service or an elevated Throne on Windows |
| `unsupported` | This system cannot run it, such as Linux without nftables, or a Linux service that grants capabilities instead of running as root. On Linux also when the core cannot adopt the kill switch's group, 1950896719: that needs CAP_SETGID and that GID mapped in the core's user namespace, which rootless and user-namespaced containers lack. Throne then keeps its core running without the group, and the kill switch keeps blocking traffic. With this code, `capabilities.kill_switch` is false |
| `overridden` | Headless Throne was started with `--no-kill-switch` or `THRONE_KILL_SWITCH=0`: the kill switch stays off for that run |

Other codes, such as `timeout` or `crash`, name a failure that may pass when Throne starts
again.

## Rule-sets and geo files

### `POST /v1/rule-sets/update`

Makes the core download the remote rule-sets of the running profile again. It takes the
[waiting](#waiting) fields, with a default `timeout` of 90. If it passes first, the
response is 202 with `{"state": "updating"}` and the update carries on.

```json
{"updated": 3, "failures": [{"tag": "geosite-x", "error": "..."}]}
```

Returns how many rule-sets were updated, and the ones that failed.

Errors: `not_running` (no profile runs, so no rule-set is loaded), `core_unavailable`,
`busy` (an update is already running).

### `GET /v1/geo-assets`

```json
{"files": [{"name": "geoip.dat", "url": "https://...", "present": true, "size": 1234, "updated_at": 1760000000}]}
```

Returns the geoip and geosite files: `url` is where Throne downloads the file from,
`updated_at` when the file last changed. `size` and `updated_at` are `0` when the file is
not `present`.

### `POST /v1/geo-assets/update`

```json
{"files": ["geoip", "geosite"], "only_missing": false}
```

Downloads the files one after another. `files` defaults to both; with `only_missing`
Throne skips the files that are present. It takes the [waiting](#waiting) fields, with a
default `timeout` of 300. If it passes first, the response is 202 with
`{"state": "downloading"}` and the downloads carry on.

```json
{"files": [{"name": "geoip.dat", "ok": true}, {"name": "geosite.dat", "ok": false, "error": "..."}]}
```

Returns the outcome for each file. A skipped file has `"ok": true, "skipped": true`.

Errors: `bad_request`, `busy` (one of the files is already downloading).

## Backup and restore

Every request here is an admin request. Archives are Throne's `.thrbackup` files, the same
ones its settings window writes and reads. **They contain credentials in plain text**:
those of the profiles and the OTP secrets. Keep them where only you can read them.

An archive holds some of these parts:

| Part | |
|---|---|
| `profiles` | Groups and profiles |
| `routes` | Routing profiles and their rules |
| `settings` | Settings |
| `otp` | OTP accounts, secrets included |
| `ip_lists` | IP lists and IP scans |
| `icons` | Icon files |

Messages from a client are at most 1 MiB, so archives travel in chunks: a backup is built
in Throne and then read in chunks, a restore is uploaded in chunks and then applied.

### `POST /v1/backups`

```json
{"parts": ["profiles", "routes"]}
```

Builds an archive of `parts`, default every part. The request takes the
[waiting](#waiting) fields, with a default `timeout` of 120.

Returns `{"id": 1, "size": 123456, "sha256": "<hex>", "parts": ["profiles", "routes"]}`:
the archive's size in bytes, its SHA-256 and the parts it holds. Those are `parts`, except
`icons` when Throne has no icon files. If `timeout` passes first, the response is 202 with
`{"id": 1, "state": "building"}`, and `GET /v1/backups/{id}` answers `busy` until the
archive is built. If the build then fails, Throne logs why and discards the backup, and
`GET` answers `not_found`.

Throne keeps at most 2 backups; a third request is `busy`. A backup expires 10 minutes
after the last request for it.

Errors: `bad_request` (`parts` is not a list of part names, is empty, or names an unknown
part), `busy`, `internal` (the build failed, also when there is nothing to back up, such as
`icons` alone without icon files; Throne discards the backup).

### `GET /v1/backups/{id}`

| Parameter | |
|---|---|
| `offset` | Default `0` |
| `length` | Default `4194304` (4 MiB), at most `16777216` (16 MiB) |

```json
{"id": 1, "size": 123456, "sha256": "<hex>", "parts": ["profiles", "routes"], "offset": 0, "data": "<base64>", "eof": true}
```

Returns up to `length` bytes of the archive from `offset` in `data`. `eof` is true when they
reach the end. Read from offset 0 until `eof`, then check the size and the SHA-256.
`size`, `sha256` and `parts` describe the whole archive, as the response of
`POST /v1/backups` does, so a client whose `POST` got 202 learns them here.

Errors: `bad_request` (`offset` or `length` is not an integer or out of range, or `offset`
is beyond the size), `not_found`, `busy` (the archive is still being built).

### `DELETE /v1/backups/{id}`

Deletes the backup. Returns `{"deleted": 1}`. A backup that is still being built is deleted
too, and a `POST /v1/backups` that still waits for it gets `not_found`.

### `POST /v1/restores`

```json
{"size": 123456, "sha256": "<hex>"}
```

Opens an upload of an archive of this size, in bytes, and SHA-256. Returns
`{"id": 2, "chunk_limit": 524288}`: the most bytes a chunk may hold.

An archive is at most 1 GiB. Throne keeps at most 2 open uploads. An upload expires 10
minutes after the last request for it.

Errors: `bad_request` (no `size` or `sha256`, a `size` below 1, a `sha256` that is not 64
hexadecimal digits), `too_large`, `busy`.

### `PUT /v1/restores/{id}`

```json
{"offset": 0, "data": "<base64>"}
```

Sends the next chunk: `data` decodes to 1 to `chunk_limit` bytes, and `offset` is the
number of bytes Throne has received so far. Returns `{"id": 2, "size": 123456, "received": 524288}`.

When the chunk completes the upload, Throne checks the SHA-256 and reads the archive, and
the request takes the [waiting](#waiting) fields, with a default `timeout` of 120. Throne
reads the waiting fields of every chunk, but only the last one waits. The response then
adds the archive:

```json
{
  "id": 2,
  "size": 123456,
  "received": 123456,
  "archive": {"created_at": "Thu Oct 16 15:04:05 2026", "platform": "linux", "parts": ["profiles", "routes", "settings"]}
}
```

`created_at` is when the archive was made, as Throne wrote it: local time without a time
zone, for people to read. `platform` is the system that made it: `winnt`, `linux` or
`darwin`. `parts` are the parts the archive holds.

If `timeout` passes first, the response is 202 with `{"id": 2, "state": "inspecting"}`, and
Throne goes on checking: follow it with `GET /v1/restores/{id}`. An archive that Throne
refuses after a 202 stays, in state `failed`, until it is deleted or expires; `PUT` and
`apply` on it answer `invalid_archive`.

Errors: `bad_request` (also for a chunk that ends past `size`, or an upload that is
already complete), `not_found`, `bad_offset` (the message names the offset Throne
expects), `busy` (the archive is being checked), `checksum_mismatch` (the archive does not
match `sha256`; Throne discards the upload), `invalid_archive` (Throne cannot restore from
the archive; it discards the upload, unless it answered 202 first).

### `GET /v1/restores/{id}`

Returns the upload as it stands:

```json
{"id": 2, "size": 123456, "received": 123456, "state": "complete", "archive": {"created_at": "...", "platform": "linux", "parts": ["profiles"]}}
{"id": 2, "size": 123456, "received": 123456, "state": "failed", "error": "The archive holds nothing Throne can restore"}
```

| `state` | |
|---|---|
| `uploading` | Throne waits for more chunks |
| `inspecting` | Throne checks the archive |
| `complete` | The archive is checked; `archive` describes it, as in the response of `PUT` |
| `failed` | Throne refused the archive after answering its last chunk with 202; `error` says why |

`archive` is present only in state `complete`, and `error` only in state `failed`.

Errors: `not_found`.

### `POST /v1/restores/{id}/apply`

```json
{"parts": ["profiles", "routes", "otp", "ip_lists"]}
```

Replaces the chosen parts of Throne's data with the archive's, then restarts Throne.
`parts` defaults to the archive's parts except `settings` and `icons`; an archive that holds
only those two needs `parts`. Throne stops the running profile first. The request takes the
[waiting](#waiting) fields, with a default `timeout` of 60. If it passes first, the
response is 202 with `{"id": 2, "state": "applying"}` and the restore carries on; if it
then fails, Throne logs why.

A `settings` restore keeps the settings that belong to this device: the Throne API, the
inbound and the ports, Tun, the kill switch, whether to remember the last profile, NTP,
logging, the system proxy, the integration with the system and the paths. Ids that no
longer resolve are reset: the remembered profile, the group Throne shows and the active
routing profile.

A restore through the API brings only what an import would accept. When the chosen parts
hold profiles that would run programs or read files on this device, such as Extra Core
profiles or configurations with settings that an import removes, or subscription or remote
routing profile URLs that are not http or https, Throne answers `invalid_archive` and
replaces nothing; the upload stays until it is deleted or expires. Restore such an archive
in Throne's window.

Returns `{"restored": ["profiles", "routes"], "skipped_rules": 0, "restarting": true}`.
`skipped_rules` counts the routing rules of the archive that this Throne could not read.
Then Throne restarts, and until it is back every request, to any path, gets 503
`restarting`. A restore in Throne's window does the same: once it has replaced the data,
every request gets `restarting` until Throne has restarted. Headless Throne then exits or
starts again on its own, as [`POST /v1/restart`](#post-v1restart) describes.

Errors: `not_found`, `incomplete` (the upload is not complete), `invalid_archive` (Throne
refused the archive when it checked it, or the archive holds what a restore through the API
may not bring, as above), `busy` (the archive is still being checked, a profile is starting
or stopping, a test or a subscription update is running), `editing` (an editor window is
open in Throne), `bad_request` (a malformed `parts`, a part the archive lacks, or no part to
restore), `internal` (the restore failed; the upload stays for another try), `exiting`.

### `DELETE /v1/restores/{id}`

Discards the upload. Returns `{"deleted": 2}`. An upload whose archive is being checked is
discarded too, and a `PUT` that still waits for the check gets `not_found`.

## Program

### `POST /v1/restart`

Admin. Restarts Throne. Throne asks nothing in its window. Responds 202 at once with
`{"restarting": true, "resume": true}`; `resume` says whether Throne starts the running
profile again after the restart, which it does when it remembers the last profile.

[Headless Throne](#headless-throne) starts the running profile again unless it was started
with `--no-resume`, whatever `remember_enable` says. It restarts as `--restart-mode` says:
`exit` exits with status 75, for its service manager to start it again; `exec` starts
Throne again in the same process on Linux and macOS, and on Windows starts a new Throne
process and exits with status 0 (a Windows service always exits as `exit` does). The
default, `auto`, exits when a service manager such as systemd, launchd or the Windows
Service Control Manager, or the init of a container, started Throne, and starts it again
otherwise, as from a terminal. Either way the connection drops; once Throne listens again,
requests get `starting` until it has started.

Errors: `busy` (a profile is starting or stopping, as while a restore stops it), `exiting`.

### `POST /v1/shutdown`

Admin. Exits Throne, as Exit in its window does. Responds 202 at once with
`{"stopping": true}`.

[Headless Throne](#headless-throne) exits with status 0. A service manager that restarts it
only after a failure, such as systemd with `Restart=on-failure`, leaves it stopped; a
container restart policy of `always` or `unless-stopped` starts it again.

Errors: `busy` (a profile is starting or stopping, as while a restore stops it), `exiting`.

## Errors

| Code | Status | |
|---|---|---|
| `bad_request` | 400 | The request or its body is malformed |
| `no_profiles` | 400 | The selection matched no profile |
| `forbidden` | 403 | An admin request from another device, while Throne accepts them only from its own: **Allow management from the local network** in Throne, or `--api-admin-lan` in headless Throne, accepts them |
| `not_found` | 404 | Unknown path, profile, group, routing profile, job, backup or upload |
| `method_not_allowed` | 405 | The path exists, the method does not |
| `busy` | 409 | Another start, stop, test, update, backup or restore is in progress |
| `superseded` | 409 | A newer start replaced this one before it ran |
| `group_archived` | 409 | The group is archived |
| `kill_switch_inactive` | 409 | The kill switch is on but not working, so Throne refuses to start profiles; the message says why (see [Kill switch](#kill-switch)) |
| `kill_switch_blocked` | 409 | The kill switch could not allow the profile's extra core |
| `mixed_inbound_disabled` | 409 | The system proxy needs Throne's mixed inbound |
| `system_proxy_unsupported` | 409 | Headless Throne never sets the system proxy |
| `tun_conflict` | 409 | Something else on this computer holds Tun, such as another Throne; the message says what (see [One Tun per computer](#one-tun-per-computer)) |
| `no_working_profile` | 409 | No candidate passed the latency test |
| `stats_disabled` | 409 | Traffic statistics are turned off |
| `editing` | 409 | What the request would change is open in an editor in Throne |
| `last_group` | 409 | Throne keeps at least one group |
| `subscription_group` | 409 | Profiles imported into a subscription group are removed by its next update |
| `not_subscription` | 409 | The group has no subscription |
| `not_running` | 409 | No profile runs |
| `incomplete` | 409 | The upload is not complete |
| `bad_offset` | 409 | The chunk does not start where the upload stands |
| `needs_privileges` | 412 | Tun needs privileges that Throne does not have |
| `too_large` | 413 | The archive is larger than 1 GiB |
| `build_failed` | 422 | Throne could not build the profile's configuration |
| `geo_assets_missing` | 422 | The profile needs geoip or geosite files that are missing |
| `strict_route_unavailable` | 422 | Windows could not enable strict routing |
| `tun_failed` | 422 | The Tun interface could not be configured |
| `start_failed` | 422 | The core rejected the configuration |
| `nothing_imported` | 422 | No profile could be imported |
| `checksum_mismatch` | 422 | The uploaded archive does not match its SHA-256 |
| `invalid_archive` | 422 | The uploaded file is not an archive Throne can restore from |
| `internal` | 500 | Something failed inside Throne |
| `fetch_failed` | 502 | A download failed |
| `core_unavailable` | 503 | Throne's core process is not reachable |
| `exiting` | 503 | Throne is shutting down |
| `restarting` | 503 | Throne restarts after a restore, made through the API or in its window |
| `starting` | 503 | Headless Throne has not finished starting; try again a moment later |
