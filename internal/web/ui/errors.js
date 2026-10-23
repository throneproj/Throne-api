const MESSAGES = {
  network: 'Cannot reach the web UI server.',
  bad_response: 'The server sent a response the web UI cannot read.',
  unauthenticated: 'Your session has ended. Sign in again.',
  csrf: 'The web UI server refused the request because it came from another origin. Open the web UI at the address the server serves it on.',
  bad_password: 'Wrong password.',
  rate_limited: 'Too many failed sign-ins. Wait a moment and try again.',
  password_file: 'The password comes from a password file (--password-file). Change it there.',
  throne_unreachable: 'Throne is not reachable.',
  throne_rejected: 'Throne refused the connection. The API key is probably wrong, or Throne blocks this address for a minute after failed attempts.',
  throne_timeout: 'Throne did not answer in time. The work may carry on in Throne.',
  bad_request: 'Throne refused the request.',
  no_profiles: 'The selection matched no profile.',
  forbidden: "This needs admin rights that the web UI's connection to Throne does not have.",
  not_found: 'Not found. It may have been deleted.',
  method_not_allowed: 'This Throne does not support the request. It may be older than this web UI.',
  busy: 'Throne is busy with another start, stop, test, update, backup or restore. Try again in a moment.',
  superseded: 'A newer start replaced this one.',
  group_archived: 'The group is archived.',
  kill_switch_inactive: 'The kill switch is on but not working, so Throne refuses to start profiles.',
  kill_switch_blocked: "The kill switch could not allow the profile's extra core.",
  mixed_inbound_disabled: "The system proxy needs Throne's mixed inbound. Turn the inbound on first.",
  system_proxy_unsupported: 'Headless Throne never sets the system proxy.',
  tun_conflict: 'Something else on this computer holds Tun, such as another Throne.',
  no_working_profile: 'No profile passed the latency test.',
  stats_disabled: 'Traffic statistics are turned off in Throne.',
  editing: "This is open in an editor in Throne's window. Close the editor and try again.",
  last_group: 'Throne keeps at least one group.',
  subscription_group: 'Profiles imported into a subscription group are removed by its next update. Choose another group.',
  not_subscription: 'The group has no subscription.',
  not_running: 'No profile is running.',
  incomplete: 'The upload is not complete.',
  bad_offset: 'The upload got out of step. Upload the file again.',
  needs_privileges: 'Tun needs privileges that Throne does not have. Grant them in Throne once, or run headless Throne with them (as root, or with NET_ADMIN and /dev/net/tun in a container).',
  too_large: 'It is too large to send.',
  build_failed: "Throne could not build the profile's configuration.",
  geo_assets_missing: 'The profile needs geoip or geosite files that are missing. Download them in Settings.',
  strict_route_unavailable: 'Windows could not enable strict routing.',
  tun_failed: 'The Tun interface could not be configured.',
  start_failed: 'The core rejected the configuration.',
  nothing_imported: 'No profile could be imported.',
  checksum_mismatch: 'The uploaded archive is damaged (checksum mismatch).',
  invalid_archive: 'Throne cannot restore from this file.',
  internal: 'Something failed inside Throne.',
  fetch_failed: 'A download failed.',
  core_unavailable: "Throne's core process is not reachable.",
  exiting: 'Throne is shutting down.',
  restarting: 'Throne is restarting.',
  starting: 'Throne is still starting. Try again in a moment.',
};

export function describeError(err) {
  if (!err) return { title: '', detail: '' };
  const code = err.code || '';
  const detail = String(err.detail || '').trim();
  const title = MESSAGES[code];
  if (title) return { title, detail: detail && detail !== code && detail !== title ? detail : '' };
  if (detail) return { title: detail, detail: '' };
  if (err.status) return { title: `The request failed (${code || `HTTP ${err.status}`}).`, detail: '' };
  return { title: String(err.message || err), detail: '' };
}

export function errorText(err) {
  const { title, detail } = describeError(err);
  return detail ? `${title} ${detail}` : title;
}

export const STATE_LABELS = {
  idle: 'Idle',
  connecting: 'Connecting…',
  running: 'Connected',
  stopping: 'Stopping…',
};

export const DEGRADED = {
  core_gave_up: 'The core kept failing and Throne gave up on it for now.',
  core_unavailable: "Throne's core process is not up.",
  kill_switch_failed: 'The kill switch is on and failed, so Throne refuses to start profiles.',
  api_unavailable: 'The Throne API does not listen.',
  tun_unavailable: 'Tun mode was on when Throne last stopped and cannot be turned on now, so the profile was not started. Turn Tun mode off, then connect.',
  resume_suppressed: 'Throne did not resume the last profile: it stopped unexpectedly three starts in a row. Connect it yourself; once it has run for a minute this clears.',
  vpn_auth_failed: "The VPN server rejected the profile's credentials, and Throne cannot ask for others.",
  vpn_challenge_pending: 'The VPN server asks for an answer, such as a one-time code, that Throne cannot give on its own.',
};

export const KILL_SWITCH_ERRORS = {
  privilege: 'Throne lacks the privileges it needs: root on Linux and macOS, a service or an elevated Throne on Windows.',
  unsupported: 'This system cannot run it, such as Linux without nftables or a rootless container.',
  config: 'Its configuration is invalid.',
  overridden: 'Throne was started with --no-kill-switch, so it stays off for this run.',
  tampered: 'Its firewall rules were changed by something else.',
  crash: 'It crashed.',
  timeout: 'It did not arm in time.',
  internal: 'An internal error stopped it.',
};

export const ADMIN_HELP = "The web UI's connection to Throne lacks admin rights, so Throne refuses imports, deletions, settings changes, backups, restores, the log, restart and shut down. Start headless Throne with --api-admin-lan (in Throne with a window: “Allow management from the local network” in its API settings), or run thronectl web on Throne's own machine.";
