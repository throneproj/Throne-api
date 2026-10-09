# Throne API, version 1

The requests Throne answers over the [wire protocol](protocol.md). Each request is a
`method` and a `path`, with an optional `query` and `body`, exactly as protocol.md describes.

The API controls the connection, Tun and system proxy modes, the routing profile and tests,
and it reads profiles and statistics. It never returns profile credentials, outbound
configurations or subscription URLs, and it cannot import, edit or delete anything.

## Conventions

- IDs are integers. `-1` means "none".
- Times are Unix seconds. `0` means "never" or "unknown".
- Byte counts are integers. Live rates are bytes per second; speed-test results are bits
  per second.
- Lists come wrapped in an object, such as `{"profiles": [...]}`, so fields can be added
  later.
- Unknown fields in a body and unknown query parameters are ignored.
- Throne writes every request that changes something to its log, with the client address.

### Waiting

Requests that start, stop or restart the connection take two optional body fields:

| Field | Type | Default | |
|---|---|---|---|
| `wait` | bool | `true` | Respond when the change has finished or failed |
| `timeout` | number | `30` | Seconds to wait, 1 to 300 |

With `wait`, the response arrives once the change succeeded (200) or failed (an error). If
`timeout` passes first, the response is 202 with the current [Status](#status), and the
change carries on in Throne. Poll `GET /v1/status` to follow it. With `"wait": false` a
change responds 202 at once. A request that changes nothing, such as stopping when nothing
runs, responds 200 either way.

While a profile is starting or stopping, requests that would restart it respond `busy`.

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
  "version": "1.4.0"
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
the current Status. While a profile is starting the response is `busy`.

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

Errors: `not_found`, plus the errors of `PUT /v1/connection` for the restart.

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

Errors: `stats_disabled` when traffic statistics are turned off in Throne.

## Errors

| Code | Status | |
|---|---|---|
| `bad_request` | 400 | The request or its body is malformed |
| `no_profiles` | 400 | The selection matched no profile |
| `not_found` | 404 | Unknown path, profile, group, routing profile or job |
| `method_not_allowed` | 405 | The path exists, the method does not |
| `busy` | 409 | Another start, stop or test is in progress |
| `superseded` | 409 | A newer start replaced this one before it ran |
| `group_archived` | 409 | The profile's group is archived |
| `kill_switch_inactive` | 409 | The kill switch is on but not working, so Throne refuses to start profiles |
| `kill_switch_blocked` | 409 | The kill switch could not allow the profile's extra core |
| `mixed_inbound_disabled` | 409 | The system proxy needs Throne's mixed inbound |
| `no_working_profile` | 409 | No candidate passed the latency test |
| `stats_disabled` | 409 | Traffic statistics are turned off |
| `needs_privileges` | 412 | Tun needs privileges that Throne does not have |
| `build_failed` | 422 | Throne could not build the profile's configuration |
| `geo_assets_missing` | 422 | The profile needs geoip or geosite files that are missing |
| `strict_route_unavailable` | 422 | Windows could not enable strict routing |
| `tun_failed` | 422 | The Tun interface could not be configured |
| `start_failed` | 422 | The core rejected the configuration |
| `internal` | 500 | Something failed inside Throne |
| `core_unavailable` | 503 | Throne's core process is not reachable |
| `exiting` | 503 | Throne is shutting down |
