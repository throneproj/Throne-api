# Throne API

The Throne API lets scripts on the same machine or the LAN control
[Throne](https://github.com/throneproj/Throne): start and stop profiles, switch Tun mode and
the system proxy, change the routing profile, run latency, IP and speed tests, and read
profiles and traffic statistics. It also manages Throne: imports profiles, adds and updates
subscriptions, deletes groups and profiles, changes settings, backs up and restores, and
restarts or shuts down Throne. Every request is encrypted and authenticated with a key that
Throne generates.

This repository holds:

- [`spec/`](spec): the [wire protocol](spec/protocol.md), the [requests](spec/api.md) and
  [test vectors](spec/vectors/v1.json);
- `thronectl`, a command-line tool that also serves a [web UI](#web-ui);
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

## Headless Throne

`Throne --headless` runs Throne without a window, as a service or in a container. Its API is
on from the start: it listens on `127.0.0.1:9095` and keeps the key in `config/api.key` in
its data directory, readable only by the user Throne runs as. `Throne --print-connection`
prints the shell commands that export `THRONE_ADDR` and `THRONE_KEY_FILE` for it, never the
key itself. Run it through `eval` in sh, bash or zsh, as the user Throne runs as and with
the same `--data-dir`:

```sh
eval "$(Throne --print-connection)"
thronectl status
```

In PowerShell, set `$env:THRONE_ADDR` and `$env:THRONE_KEY_FILE` to the values it prints.
It exits with status 2 when the API is off or failed to listen, or when its key is not in a
file.

Throne running as root keeps its data in `/var/lib/throne` on Linux, and the key file is
root's. `sudo` drops `THRONE_KEY_FILE` from the environment, so run thronectl with `sudo`
and `--key-file` (and `--addr` when Throne listens elsewhere than `127.0.0.1:9095`), or
copy the key to a file of your own:

```sh
sudo Throne --print-connection
# export THRONE_ADDR=127.0.0.1:9095
# export THRONE_KEY_FILE=/var/lib/throne/config/api.key
sudo thronectl --key-file /var/lib/throne/config/api.key status

# or, as yourself
mkdir -p ~/.config/throne
sudo install -m 600 -o "$USER" /var/lib/throne/config/api.key ~/.config/throne/api-key
export THRONE_KEY_FILE=~/.config/throne/api-key
thronectl status
```

In a container, run thronectl inside it, such as with `docker exec <container> thronectl
status`, and set `THRONE_KEY_FILE` in the container's environment. The listener is on the
container's loopback, and admin requests are answered only from there.

`--api-listen`, `--api-key-file`, `--api-admin-lan` and `--api-allow` (or `THRONE_API_LISTEN`,
`THRONE_API_KEY_FILE`, `THRONE_API_ADMIN_LAN` and `THRONE_API_ALLOW`) change the listener;
`--api-listen off` turns the API off. An empty `--api-allow` or `THRONE_API_ALLOW`, or one of
only commas and spaces, makes Throne exit with status 64; leave `THRONE_API_ALLOW` unset to
let in every device. Headless Throne never sets the system proxy and
answers `starting` until it has started. `thronectl restart` makes it exit with status 75,
for its service manager to start it again, or start again when it was started from a
terminal (in the same process; on Windows as a new process); `thronectl shutdown` makes it
exit with status 0.
`Throne --health` prints `ok`, `degraded` and the state of the API's listener (`api`) as
one line of JSON, and exits with status 0 when Throne works.
[api.md](spec/api.md#headless-throne) has the details.

`Throne --check`, run the same way, tells whether headless Throne can run as it is set up,
and changes nothing: the data directory, TLS, the core, the privileges that Tun and the kill
switch need, the ports, and the service manager, each on one `[ OK ]`, `[WARN]`, `[FAIL]` or
`[INFO]` line with a fix where there is one. It exits with status 0 when nothing failed, and
1 otherwise; missing privileges for Tun or the kill switch are only warnings.

Under systemd, run it as a `Type=notify` service: Throne reports when it has started, keeps
a status line for `systemctl status`, and with `WatchdogSec` reports that its main loop runs.
It also watches its own progress: it exits with status 71 when a profile has been starting
for 180 seconds or stopping for 90, and with 70 when its core has not been usable for 60
seconds, for its service manager to start it again. After three starts in a row without a
working core it stays up and restarts the core instead. [api.md](spec/api.md#systemd) has a
unit.

One Throne at a time can run Tun on a computer. `thronectl tun on` fails with `tun_conflict`
while something else holds Tun, such as another Throne, and `thronectl status` shows what.
When a network of the computer, such as a Docker bridge, already uses Tun's address, give
Tun another range: `thronectl settings set tun_ipv4_cidr=172.29.0.1/24`.
A Throne with a window warns when a headless Throne runs on the same computer, refuses Tun
while it runs, and does not update itself while it runs from the same installation.
[api.md](spec/api.md#one-tun-per-computer) has the details.

The kill switch lets clients open new connections to the Throne API wherever they connect
from: loopback is always allowed, and for a listener on another address it lets out the
replies from the API's port, so also those of another program on that port at another
address. On macOS, API connections already open when the kill switch is turned on are cut
once, and clients connect again. [api.md](spec/api.md#kill-switch) has the details.

If saved settings still keep headless Throne unreachable, such as Tun, or a kill switch
that ends your SSH session, start it once with `--no-resume --no-kill-switch` (or
`THRONE_RESUME=0` and `THRONE_KILL_SWITCH=0`): no profile starts and the kill switch stays
off for that run, so thronectl on its device can change the settings, such as with
`thronectl settings set kill_switch=false` or `thronectl tun off`. After three starts in a
row without a working core, Throne stays up instead of exiting with status 70 or 71, so the
API stays reachable. [api.md](spec/api.md#recovery) has the
details.

## thronectl

```sh
go install github.com/throneproj/Throne-api/cmd/thronectl@latest
```

or download a binary for Linux, Windows, macOS or FreeBSD from
[Releases](https://github.com/throneproj/Throne-api/releases).

thronectl reads the address from `THRONE_ADDR` and the key from `THRONE_KEY`, or from the
file that `THRONE_KEY_FILE` names; for headless Throne, `eval "$(Throne --print-connection)"`
exports both variables. It never takes the key itself as an argument.

| Flag | |
|---|---|
| `--addr host:port` | Throne's API address. Default: `$THRONE_ADDR`, else `127.0.0.1:9095` |
| `--key-file file` | Read the key from this file |
| `--json` | Print the response body as JSON instead of a table |
| `--timeout 30s` | Give up after this long; `0` waits as long as Throne may take |

Flags work before or after the command.

| Command | |
|---|---|
| `status` | State, running profile, modes, routing profile, version, and a Tun conflict when there is one |
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
| `logs [-n N] [-f]` | The last N lines of Throne's log, 200 by default. `-f` then prints new lines until interrupted, through lost connections and restarts of Throne. With `--json`, a JSON object per line |
| `import [--group N] (FILE \| - \| --url URL)` | Import share links, a subscription body, or a Clash, sing-box, Xray, WireGuard or OpenVPN configuration from a file of up to 900 KiB or standard input (`-`), or have Throne download a URL once. Default group: the one Throne shows |
| `sub add <url> [--name NAME] [--no-auto-update] [--no-update]` | Add a subscription group, named after the URL's host by default, and download it |
| `sub update (<id>... \| --all)` | Update subscription groups, or all that are not archived |
| `group add <name>`, `group rm <id>` | Add a group; delete a group with its profiles |
| `profile rm (<id>... \| --group N [--name REGEX])` | Delete profiles |
| `settings [get] [KEY...]`, `settings set KEY=VALUE...` | Show or change the [settings](spec/api.md#settings) the API manages. `true`, `false` and numbers are sent as such, anything else as text |
| `backup create [-o FILE] [--parts LIST]` | Write a backup archive, by default to `throne-backup-<time>.thrbackup` |
| `backup restore FILE [--parts LIST] [--yes]` | Show an archive, ask, restore it and restart Throne. Default parts: `profiles,routes,otp,ip_lists` |
| `rulesets update` | Update the remote rule-sets of the running profile |
| `geo`, `geo update [geoip \| geosite]... [--only-missing]` | Show the geo files, download them again |
| `restart`, `shutdown [--yes]` | Restart Throne; make it exit, after asking |
| `web [--listen ADDR] ...`, `web passwd` | Serve the [web UI](#web-ui); set its password |
| `keygen [-o FILE]` | Generate a new key: print it, or write it to a new file that only you can read |
| `call <METHOD> <PATH> [key=value ...] [--body JSON]` | Send any request from [api.md](spec/api.md) and print the response body |
| `completion bash \| zsh \| fish \| powershell` | Print a [shell completion](#shell-completion) script |
| `version` | Print thronectl's version |

`thronectl help <command>` describes a command. `--name` is a case-insensitive regular
expression; quote it in the shell. `--parts` takes a comma-separated list of `profiles`,
`routes`, `settings`, `otp`, `ip_lists` (or `ip-lists`) and `icons`.

Requests that start or stop a profile wait for the result, up to `--timeout`. If Throne needs
longer it answers with the current state, such as `connecting`, and finishes on its own;
thronectl exits with 0. Imports, updates, deletes and downloads wait the same way, and say so
when they are still running in Throne; `--no-wait` returns at once. Tests and geo downloads
can take minutes, so give them a longer `--timeout`; a test that outlasts it keeps running in
Throne (`thronectl job <id>`).

`logs`, `import`, `sub add`, `group`, `profile rm`, `settings set`, `backup`, `restart` and
`shutdown` send admin requests. Throne answers them only on its own device, unless
**Allow management from the local network** is on in its Throne API settings, or headless
Throne was started with `--api-admin-lan`; elsewhere they fail with `forbidden`. Backup
archives hold your profiles' credentials and OTP secrets in plain text: thronectl creates
them readable only by you, so keep them that way.
Throne refuses to `backup restore` an archive whose profiles would run programs or read
files on its device, such as Extra Core profiles (`invalid_archive`); restore such an archive
in Throne's window. `backup restore` and `shutdown` ask before they act; without a terminal
to ask on, they need `--yes`.

Exit status: 0 success, 1 error from Throne (printed as `error: <code>: <message>`), an update
of a subscription, a rule-set or a geo file that failed, or a confirmation answered no, 2
usage error, 3 no connection, a rejected key, or another error such as a file that cannot be
read.

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
thronectl logs -f
thronectl call GET /v1/profiles group=3
thronectl import links.txt --group 3
thronectl sub add 'https://sub.example.com/abc' --name Provider
thronectl --timeout 2m sub update --all
thronectl settings set log_level=debug test_url=https://cp.cloudflare.com/
thronectl backup create -o throne.thrbackup
thronectl backup restore throne.thrbackup --parts profiles,routes
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

### Shell completion

`thronectl completion <shell>` prints a script that completes commands, flags, subcommands,
settings, backup parts and the other fixed values in bash, zsh, fish and PowerShell. When
Throne answers within a second, it also completes the IDs of profiles, groups, routing
profiles and tests, with their names. It reaches Throne as the other commands do: through
`THRONE_ADDR` and the key, or `--addr` and `--key-file` on the line.

```sh
# bash: in ~/.bashrc (on macOS, ~/.bash_profile)
eval "$(thronectl completion bash)"
# zsh: in ~/.zshrc
autoload -Uz compinit && compinit
source <(thronectl completion zsh)
# fish
thronectl completion fish > ~/.config/fish/completions/thronectl.fish
```

```powershell
# In $PROFILE
thronectl completion powershell | Out-String | Invoke-Expression
```

bash can also load the script from bash-completion's directory: save it as
`~/.local/share/bash-completion/completions/thronectl`. zsh can load it from `$fpath`:

```sh
mkdir -p ~/.zfunc && thronectl completion zsh > ~/.zfunc/_thronectl
# and in ~/.zshrc, before compinit:
fpath=(~/.zfunc $fpath)
```

The scripts ask thronectl what completes, so they keep up with new versions of it.

## Web UI

`thronectl web` serves a web UI for Throne: the connection and its modes, profiles and
tests, imports and subscriptions, traffic, settings, backup and restore, and the live log.
The browser talks to thronectl, and thronectl to Throne through the Throne API, at the
address and with the key that the other commands use; Throne itself serves no web page.

```sh
thronectl web                       # serves http://127.0.0.1:9096/
ssh -L 9096:127.0.0.1:9096 server   # from another computer; then open http://localhost:9096/
thronectl web --listen '*:9096'     # on every address of this device, over HTTPS
thronectl web passwd                # set a new password
```

It listens on `127.0.0.1:9096` by default, which only this device reaches; from elsewhere,
use an SSH tunnel as above. On a network address it serves HTTPS with a self-signed
certificate that it creates in its state directory, and prints the certificate's SHA-256
fingerprint: compare it with the one your browser shows before you accept the certificate.
`--tls-cert` and `--tls-key` give it a certificate of your own. `--tls off` serves plain
HTTP, which carries the password and the session in the clear: use it only behind a TLS
reverse proxy.

Signing in takes a password. The first start creates one and prints it once. `thronectl web
passwd` sets a new one, read from standard input or asked for twice on a terminal, of at
least 8 characters; a running `thronectl web` then signs everyone out within 5 seconds.
`--password-file` takes the password from a file instead, such as a Docker secret, and
`passwd` then refuses.

| Flag | Variable | Default |
|---|---|---|
| `--listen ADDR` | `THRONE_WEB_LISTEN` | `127.0.0.1:9096`. Takes `PORT`, `IP:PORT`, `[IPv6]:PORT` and `*:PORT` (every address), as Throne's `--api-listen` does |
| `--state-dir DIR` | `THRONE_WEB_STATE_DIR` | systemd's `$STATE_DIRECTORY`, else `thronectl/web` in your configuration directory, such as `~/.config/thronectl/web`. It holds the password's hash and the certificate |
| `--tls auto \| on \| off` | `THRONE_WEB_TLS` | `auto`: on, unless the address is loopback |
| `--tls-cert FILE`, `--tls-key FILE` | `THRONE_WEB_TLS_CERT`, `THRONE_WEB_TLS_KEY` | A self-signed certificate in the state directory |
| `--allow-host LIST` | `THRONE_WEB_ALLOW_HOST` | Host names that browsers may reach it by, separated by commas or spaces, such as that of a reverse proxy. On loopback it answers `localhost`, `127.0.0.1` and `::1` without them; on a network address it checks host names only when this is set |
| `--password-file FILE` | `THRONE_WEB_PASSWORD_FILE` | The password in plain text |

A flag wins over its variable. `--addr` and `--key-file` pick the Throne as for the other
commands; under systemd, `LoadCredential=api.key:/path/to/key` passes the key too.

The web UI is an admin client: it does what thronectl does, admin requests included, for
whoever signs in, so give its password only to those who may manage Throne. On Throne's own
device Throne answers its admin requests; elsewhere only with **Allow management from the
local network** on (`--api-admin-lan` in headless Throne), and the web UI says so where it
cannot act.

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
`LiveStats`, `Traffic` and `Events`. The management requests have `Logs`, `Import`,
`AddGroup`, `UpdateGroup`, `UpdateGroups`, `DeleteGroup`, `DeleteProfiles`, `Settings`,
`SetSettings`, `UpdateRuleSets`, `GeoAssets`, `UpdateGeoAssets`, `Restart` and `Shutdown`;
`Backup` writes a backup archive to an `io.Writer`, and `UploadRestore`, `RestoreStatus`,
`ApplyRestore` and `CancelRestore` restore one. `Call` sends anything else, and `Send` does
too and returns the status and body as Throne sent them. A `Client` opens one connection per
call; `Client.Dial` opens a `Session` that carries several calls over one connection.

- Requests that change something wait for the result. With a deadline in `ctx`, Throne
  gets a `timeout` that ends a little before it; when that passes, the call returns what
  Throne has so far without an error: for a connection change the current `Status`, in state
  `connecting` for example. `throne.NoWait()` returns at once.
- Admin requests, which read the log, import, delete, change settings, back up, restore,
  restart and shut down, are answered only from Throne's own device unless Throne allows
  management from the local network; elsewhere they fail with `throne.CodeForbidden`.
- `Events` and `Logs` follow Throne: call them again with the `Instance` and `Cursor` of the
  last result and a `Wait`, and Throne holds the call until something changes or a line
  follows.
- ID 0 is a valid group and profile ID. Select profiles with `throne.SelectGroup(id, regex)`
  or `throne.SelectProfiles(ids...)`.

Errors:

- Throne's error responses are `*throne.APIError` values with `Status`, `Code` and `Message`.
  `throne.ErrorCode(err)` returns the code, such as `throne.CodeBusy`.
- `throne.ErrRejected` means Throne closed the connection without answering. Throne does that
  when a request fails authentication, so the key is probably wrong, and also when it refuses
  the connection: an address outside the allowed list, too many connections, or an address
  that failed too often. After 5 failed attempts within a minute from one address, Throne
  refuses every connection from it for 60 s, so a client there that still has an old key
  locks out the others at that address, such as those behind the same NAT or those that
  reach a container through its published port. Throne skips that when a client at the
  address authenticated in the last 10 minutes, and never refuses loopback.
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
  and traffic. On Throne's own device, or from the LAN when Throne allows management from
  there, it also imports, deletes, changes settings and makes backups, which hold your
  profiles' credentials in plain text.
- Keep the key out of command lines and shell history: use a key file that only you can read,
  or `THRONE_KEY` from a secret store. thronectl never accepts the key as an argument.
- Traffic is encrypted and authenticated, on a LAN too, but there is no forward secrecy:
  whoever learns the key can read traffic recorded earlier. If the key leaks, generate a new
  one in Throne; for headless Throne, stop it, delete its key file, write a new one with
  `thronectl keygen -o <file>` and start it again. Keep the API on loopback unless you need
  it on the LAN, and there list the devices that may connect under **Allowed addresses** in
  the Throne API tab, or with `--api-allow` in headless Throne.

## License

[MIT](LICENSE).
