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
Throne API settings in Throne, it answers them from every client it lets in. Otherwise an
admin request gets 403 `forbidden`. The key is the same for both kinds.

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
| `route` | The active routing profile, or `null` if it no longer exists |
| `kill_switch` | `off`, `arming`, `armed` or `failed` |
| `core` | Whether Throne's core process is up |
| `inbound` | Throne's mixed (SOCKS and HTTP) proxy: the configured listen address and port. `enabled` is false when it is turned off |
| `version` | Throne's version |
| `capabilities` | What this Throne can do, below |
| `restart_needed` | Changes, such as settings, wait for the running profile to restart. `PUT /v1/connection` with `restart` applies them |
| `degraded` | `null`, or why Throne cannot work as set up: `core_unavailable` (the core process is not up) or `kill_switch_failed` (the kill switch is on and failed) |

| Capability | |
|---|---|
| `tun` | Throne has the privileges Tun needs, as far as it knows without asking, or is set not to ask for them |
| `kill_switch` | The kill switch works on this system |
| `headless` | Throne runs without a window |

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

Errors: `needs_privileges` (Tun needs privileges that Throne does not have; grant them in
Throne once), `mixed_inbound_disabled` (the system proxy needs Throne's mixed inbound),
plus the errors of `PUT /v1/connection` for the restart.

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
| `remember_enable` | bool | Remember the running profile and start it again when Throne starts | |
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
| `kill_switch` | bool | The kill switch | |
| `tun_strict_route` | bool | Tun strict routing | tun |
| `tun_auto_redirect` | bool | Tun auto redirect. Linux only | tun |
| `tun_mtu` | number | Tun MTU: 1000 to 10000 | tun |
| `tun_ipv6` | bool | Tun takes IPv6 traffic | tun |
| `remote_dns` | string | DNS server for proxied traffic, such as `https://8.8.8.8/dns-query` | profile |
| `direct_dns` | string | DNS server for direct traffic, such as `localhost` | profile |
| `ntp_enabled` | bool | The core keeps its time with NTP. Turning it on with an empty `ntp_interval` sets that to `30m` | profile |
| `ntp_server` | string | NTP server host, or `""` for the core's default | profile |
| `ntp_interval` | string | How often NTP runs: a duration such as `30m` or `1h` | profile |

Restart `profile`: the change takes effect when the running profile restarts. `tun`: the
same, when Tun is on. Empty: at once. Turning an automatic update off keeps its interval
for later; `GET` reads it as `0`.

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
every request gets `restarting` until Throne has restarted.

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

Errors: `busy` (a profile is starting or stopping, as while a restore stops it), `exiting`.

### `POST /v1/shutdown`

Admin. Exits Throne, as Exit in its window does. Responds 202 at once with
`{"stopping": true}`.

Errors: `busy` (a profile is starting or stopping, as while a restore stops it), `exiting`.

## Errors

| Code | Status | |
|---|---|---|
| `bad_request` | 400 | The request or its body is malformed |
| `no_profiles` | 400 | The selection matched no profile |
| `forbidden` | 403 | An admin request from another device, while Throne accepts them only from its own |
| `not_found` | 404 | Unknown path, profile, group, routing profile, job, backup or upload |
| `method_not_allowed` | 405 | The path exists, the method does not |
| `busy` | 409 | Another start, stop, test, update, backup or restore is in progress |
| `superseded` | 409 | A newer start replaced this one before it ran |
| `group_archived` | 409 | The group is archived |
| `kill_switch_inactive` | 409 | The kill switch is on but not working, so Throne refuses to start profiles |
| `kill_switch_blocked` | 409 | The kill switch could not allow the profile's extra core |
| `mixed_inbound_disabled` | 409 | The system proxy needs Throne's mixed inbound |
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
