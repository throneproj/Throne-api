# Throne API

The Throne API lets scripts on the same machine or the LAN control
[Throne](https://github.com/throneproj/Throne): start and stop profiles, switch Tun mode and
the system proxy, change the routing profile, run latency, IP and speed tests, and read
profiles and traffic statistics. Every request is encrypted and authenticated with a key that
Throne generates.

This repository holds:

- [`spec/`](spec): the [wire protocol](spec/protocol.md), the [requests](spec/api.md) and
  [test vectors](spec/vectors/v1.json);
- `thronectl`, a command-line tool;
- `github.com/throneproj/Throne-api`, a Go library (package `throne`).

## Enabling it in Throne

Open **Preferences → Integration Settings → Throne API**, enable the API, and press
**Copy for scripts**. That copies two settings, `THRONE_ADDR` (default `127.0.0.1:9095`) and
`THRONE_KEY`. Keep the key in a file that only you can read and point `THRONE_KEY_FILE` at it,
or put `THRONE_KEY` in the environment of your scripts:

```sh
export THRONE_ADDR=127.0.0.1:9095
export THRONE_KEY_FILE=~/.config/throne/api-key
```

```powershell
$env:THRONE_ADDR = "127.0.0.1:9095"
$env:THRONE_KEY_FILE = "$HOME\throne-api-key.txt"
```

## thronectl

```sh
go install github.com/throneproj/Throne-api/cmd/thronectl@latest
```

or download a binary for Linux, Windows, macOS or FreeBSD from
[Releases](https://github.com/throneproj/Throne-api/releases).

thronectl reads the address from `THRONE_ADDR` and the key from `THRONE_KEY`, or from the
file that `THRONE_KEY_FILE` names. It never takes the key itself as an argument.

| Flag | |
|---|---|
| `--addr host:port` | Throne's API address. Default: `$THRONE_ADDR`, else `127.0.0.1:9095` |
| `--key-file file` | Read the key from this file |
| `--json` | Print the response body as JSON instead of a table |
| `--timeout 30s` | Give up after this long; `0` waits as long as Throne may take |

Flags work before or after the command.

| Command | |
|---|---|
| `status` | State, running profile, modes, routing profile, version |
| `groups` | Groups and their IDs |
| `profiles [--group N] [--name REGEX] [--ids 1,2]` | Profiles with their last test results |
| `connect <id> \| last \| best [--group N] [--name REGEX] [--max-age 1h]` | Start a profile. `best` starts the one with the lowest latency, first testing those without a result younger than `--max-age`. Also takes `--restart` and `--no-wait` |
| `disconnect [--no-wait]` | Stop the running profile |
| `toggle [--no-wait]` | `connect last` when idle, else `disconnect` |
| `tun on \| off \| toggle`, `proxy on \| off \| toggle` | Tun mode, system proxy |
| `routes`, `route <id>` | List the routing profiles, activate one |
| `test latency \| ip \| speed (--group N [--name REGEX] \| <id>...) [--no-wait]` | Run a test and show the results |
| `jobs`, `job <id>`, `cancel <id>` | Recent tests, one test, stop a test |
| `clear (--group N [--name REGEX] \| <id>...)` | Clear test results |
| `live` | Current traffic rates |
| `traffic [--by profile \| app] [--since 24h]` | Traffic totals; `--since` also takes days, such as `7d` |
| `call <METHOD> <PATH> [key=value ...] [--body JSON]` | Send any request from [api.md](spec/api.md) and print the response body |
| `version` | Print thronectl's version |

`thronectl help <command>` describes a command. `--name` is a case-insensitive regular
expression; quote it in the shell.

Requests that start or stop a profile wait for the result, up to `--timeout`. If Throne needs
longer it answers with the current state, such as `connecting`, and finishes on its own;
thronectl exits with 0. Tests can take minutes, so give them a longer `--timeout`; a test that
outlasts it keeps running in Throne (`thronectl job <id>`).

Exit status: 0 success, 1 error from Throne (printed as `error: <code>: <message>`), 2 usage
error, 3 no connection or a rejected key.

### Examples

```sh
thronectl status
thronectl groups
thronectl profiles --group 3 --name 'DE|NL'
thronectl connect 12
thronectl tun on
thronectl route 2
thronectl --timeout 2m test latency --group 3
thronectl traffic --by app --since 7d
thronectl call GET /v1/profiles group=3
```

Speed-test the profiles of group 3 whose names match `DE` or `NL`, then connect the fastest
([#1818](https://github.com/throneproj/Throne/issues/1818)):

```sh
best=$(thronectl --json --timeout 15m test speed --group 3 --name 'DE|NL' |
  jq -r '[.results[] | select(.download_bps > 0)] | max_by(.download_bps) | .id // empty')
[ -n "$best" ] && thronectl connect "$best"
```

```powershell
$job = thronectl --json --timeout 15m test speed --group 3 --name 'DE|NL' | ConvertFrom-Json
$best = $job.results | Where-Object download_bps -gt 0 | Sort-Object download_bps -Descending | Select-Object -First 1
if ($best) { thronectl connect $best.id }
```

One key to connect and disconnect, for a hotkey, a macro pad or a Stream Deck
([#1925](https://github.com/throneproj/Throne/issues/1925)):

```sh
thronectl toggle
```

and another to connect the profile with the lowest latency in the group Throne shows,
retesting results older than ten minutes:

```sh
thronectl connect best --max-age 10m
```

Tools that start programs from a hotkey often do not see your shell's environment; set
`THRONE_KEY_FILE` as a user environment variable, or pass `--key-file`.

## Go library

```sh
go get github.com/throneproj/Throne-api
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	throne "github.com/throneproj/Throne-api"
)

func main() {
	c, err := throne.FromEnv() // or throne.New(addr, key), with throne.ParseKey or throne.ReadKeyFile
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	job, err := c.RunTest(ctx, throne.TestSpeed, throne.SelectGroup(3, "DE|NL"))
	if err != nil {
		log.Fatal(err)
	}
	best, fastest := -1, int64(0)
	for _, p := range job.Results {
		if p.DownloadBPS > fastest {
			best, fastest = p.ID, p.DownloadBPS
		}
	}
	if best < 0 {
		log.Fatal("no profile passed the speed test")
	}
	st, err := c.Connect(ctx, best)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(st.State)
}
```

Every request in [api.md](spec/api.md) has a method: `Status`, `Groups`, `Profiles`,
`Profile`, `Connect`, `ConnectLast`, `ConnectLowestLatency`, `Disconnect`, `SetModes`,
`Routes`, `SetRoute`, `StartTest`, `RunTest`, `Jobs`, `Job`, `CancelJob`, `ClearResults`,
`LiveStats` and `Traffic`; `Call` sends anything else. A `Client` opens one connection per
call; `Client.Dial` opens a `Session` that carries several calls over one connection.

- Requests that change the connection wait for the result. With a deadline in `ctx`, Throne
  gets a `timeout` that ends a little before it; when that passes, the call returns the
  current `Status`, in state `connecting` for example, without an error. `throne.NoWait()`
  returns at once.
- ID 0 is a valid group and profile ID. Select profiles with `throne.SelectGroup(id, regex)`
  or `throne.SelectProfiles(ids...)`.

Errors:

- Throne's error responses are `*throne.APIError` values with `Status`, `Code` and `Message`.
  `throne.ErrorCode(err)` returns the code, such as `throne.CodeBusy`.
- `throne.ErrRejected` means Throne closed the connection without answering. Throne does that
  when a request fails authentication, so the key is probably wrong, and also when it refuses
  the connection: an address outside the allowed list, or too many connections.
- Timeouts and cancellation return the `ctx` error, which `errors.Is` matches.

```go
_, err := c.Connect(ctx, 12)
switch {
case throne.ErrorCode(err) == throne.CodeBusy:
	// another profile is starting or stopping
case errors.Is(err, throne.ErrRejected):
	// wrong key
case err != nil:
	log.Fatal(err)
}
```

## Security

- The key grants every request in [api.md](spec/api.md): whoever holds it controls Throne's
  connection, modes and routing, and can read your profiles' names, addresses, test results
  and traffic.
- Keep the key out of command lines and shell history: use a key file that only you can read,
  or `THRONE_KEY` from a secret store. thronectl never accepts the key as an argument.
- Traffic is encrypted and authenticated, on a LAN too, but there is no forward secrecy:
  whoever learns the key can read traffic recorded earlier. If the key leaks, generate a new
  one in Throne. Keep the API on loopback unless you need it on the LAN, and there list the
  devices that may connect under **Allowed addresses** in the Throne API tab.

## License

[MIT](LICENSE).
