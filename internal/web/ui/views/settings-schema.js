// The settings Throne's API exposes (spec/api.md, "Settings"). restart: 'profile' applies when the running
// profile restarts, 'tun' the same while Tun is on; absent applies at once. zeroOff: 0 turns it off.
export const SETTINGS_GROUPS = [
  {
    title: 'General',
    keys: [
      { key: 'remember_enable', label: 'Remember the running profile', type: 'bool', help: 'Start it again when Throne starts. Headless Throne does that either way, unless it was started with --no-resume.' },
      { key: 'log_level', label: 'Core log level', type: 'enum', options: ['trace', 'debug', 'info', 'warn', 'error', 'fatal', 'panic'], restart: 'profile' },
      { key: 'xray_log_level', label: 'Xray log level', type: 'enum', options: ['debug', 'info', 'warning', 'error', 'none'], restart: 'profile' },
    ],
  },
  {
    title: 'Inbound',
    keys: [
      { key: 'inbound_enabled', label: 'Mixed inbound', type: 'bool', help: "Throne's SOCKS and HTTP proxy. It cannot be turned off while the system proxy is on.", restart: 'profile' },
      { key: 'inbound_address', label: 'Listen address', type: 'string', kind: 'ip', help: 'An IP address, such as 127.0.0.1 for this device only or 0.0.0.0 for every network.', restart: 'profile' },
      { key: 'inbound_port', label: 'Port', type: 'number', min: 1, max: 65535, help: "1 to 65535, and none of Throne's other ports. Changing it turns off the random port.", restart: 'profile' },
    ],
  },
  {
    title: 'Tests',
    keys: [
      { key: 'test_url', label: 'URL test address', type: 'string', kind: 'url', help: 'The http or https URL the latency test requests.' },
      { key: 'test_concurrency', label: 'Profiles tested at once', type: 'number', min: 1, max: 499 },
      { key: 'test_timeout_ms', label: 'URL test timeout', type: 'number', min: 0, max: 60000, unit: 'ms', help: "0 to 60000 milliseconds; 0 uses the core's default." },
    ],
  },
  {
    title: 'Subscriptions',
    keys: [
      { key: 'sub_auto_update', label: 'Automatic subscription updates', type: 'number', min: 30, max: 525600, zeroOff: true, unit: 'min', help: 'Minutes between updates, 30 to 525600; 0 turns them off.' },
      { key: 'sub_respect_server_interval', label: "Use the subscription's own interval", type: 'bool', help: 'Use the update interval a subscription asks for.' },
      { key: 'route_auto_update', label: 'Automatic routing profile updates', type: 'number', min: 30, max: 525600, zeroOff: true, unit: 'min', help: 'Minutes between updates of remote routing profiles, 30 to 525600; 0 turns them off.' },
    ],
  },
  {
    title: 'Tun',
    keys: [
      { key: 'tun_strict_route', label: 'Strict routing', type: 'bool', restart: 'tun' },
      { key: 'tun_auto_redirect', label: 'Auto redirect', type: 'bool', help: 'Linux only.', restart: 'tun' },
      { key: 'tun_mtu', label: 'MTU', type: 'number', min: 1000, max: 10000, restart: 'tun' },
      { key: 'tun_ipv6', label: 'IPv6', type: 'bool', help: 'Tun takes IPv6 traffic.', restart: 'tun' },
      { key: 'tun_ipv4_cidr', label: 'IPv4 address', type: 'string', kind: 'cidr4', help: "Tun's IPv4 address with its prefix length, such as 172.19.0.1/24: a range that no network of the computer uses.", restart: 'tun' },
      { key: 'tun_ipv6_cidr', label: 'IPv6 address', type: 'string', kind: 'cidr6', help: "Tun's IPv6 address with its prefix length, such as fdfe:dcba:9876::1/96, used while IPv6 is on.", restart: 'tun' },
    ],
  },
  {
    title: 'DNS',
    keys: [
      { key: 'remote_dns', label: 'Remote DNS', type: 'string', help: 'DNS server for proxied traffic, such as https://8.8.8.8/dns-query.', restart: 'profile' },
      { key: 'direct_dns', label: 'Direct DNS', type: 'string', help: 'DNS server for direct traffic, such as localhost.', restart: 'profile' },
      { key: 'dns_in_port', label: 'DNS server port', type: 'number', min: 1, max: 65535, help: "Port of the core's DNS server on 127.0.0.1 (TCP and UDP), and none of Throne's other ports.", restart: 'profile' },
    ],
  },
  {
    title: 'Core APIs',
    keys: [
      { key: 'clash_api_port', label: 'Clash API port', type: 'number', min: 1, max: 65535, zeroOff: true, help: '0 turns the Clash API off.', restart: 'profile' },
      { key: 'clash_listen_addr', label: 'Clash API listen address', type: 'string', help: 'An IP address or host name; empty for 127.0.0.1. An address other than loopback needs a secret, which is set only in Throne\'s settings window: anyone who reaches the Clash API controls the core.', restart: 'profile' },
      { key: 'singbox_api_port', label: 'sing-box API port', type: 'number', min: 1, max: 65535, zeroOff: true, help: 'On 127.0.0.1; 0 turns it off.', restart: 'profile' },
    ],
  },
  {
    title: 'NTP',
    keys: [
      { key: 'ntp_enabled', label: 'Keep time with NTP', type: 'bool', help: 'The core keeps its time with NTP.', restart: 'profile' },
      { key: 'ntp_server', label: 'NTP server', type: 'string', help: "A host; empty for the core's default.", restart: 'profile' },
      { key: 'ntp_interval', label: 'Interval', type: 'string', kind: 'duration', help: 'How often NTP runs, such as 30m or 1h.', restart: 'profile' },
    ],
  },
  {
    title: 'Kill switch',
    keys: [
      { key: 'kill_switch', label: 'Kill switch', type: 'bool' },
    ],
  },
];

export const KNOWN_KEYS = new Set(SETTINGS_GROUPS.flatMap(g => g.keys.map(k => k.key)));

const IPV4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;

function isIPv4(s) {
  return IPV4.test(s);
}

function isIPv6(s) {
  return s.includes(':') && /^[0-9a-fA-F:.]+$/.test(s);
}

// Returns { value, error } for what the user entered. Throne checks again and has the last word.
export function parseValue(def, raw) {
  if (def.type === 'bool') return { value: !!raw, error: '' };
  if (def.type === 'number') {
    const s = String(raw).trim();
    if (def.generic ? !/^-?\d+(\.\d+)?$/.test(s) : !/^-?\d+$/.test(s)) {
      return { value: raw, error: def.generic ? 'Enter a number.' : 'Enter a whole number.' };
    }
    const n = Number(s);
    if (def.zeroOff && n === 0) return { value: 0, error: '' };
    if ((def.min !== undefined && n < def.min) || (def.max !== undefined && n > def.max)) {
      return { value: n, error: `Enter ${def.min} to ${def.max}${def.zeroOff ? ', or 0 to turn it off' : ''}.` };
    }
    return { value: n, error: '' };
  }
  const s = String(raw ?? '');
  const t = s.trim();
  switch (def.kind) {
    case 'ip':
      return { value: t, error: isIPv4(t) || isIPv6(t) ? '' : 'Enter an IP address.' };
    case 'url':
      return { value: t, error: /^https?:\/\/\S+$/i.test(t) ? '' : 'Enter an http or https URL.' };
    case 'cidr4': {
      const [ip, len] = t.split('/');
      return { value: t, error: isIPv4(ip || '') && /^\d+$/.test(len || '') && Number(len) <= 32 ? '' : 'Enter an IPv4 address with a prefix length, such as 172.19.0.1/24.' };
    }
    case 'cidr6': {
      const [ip, len] = t.split('/');
      return { value: t, error: isIPv6(ip || '') && /^\d+$/.test(len || '') && Number(len) <= 128 ? '' : 'Enter an IPv6 address with a prefix length, such as fdfe:dcba:9876::1/96.' };
    }
    case 'duration':
      return { value: t, error: t === '' || /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(t) ? '' : 'Enter a duration such as 30m or 1h.' };
    default:
      return { value: s, error: '' };
  }
}

export function inferDef(key, value) {
  const type = typeof value === 'boolean' ? 'bool' : typeof value === 'number' ? 'number' : typeof value === 'string' ? 'string' : 'json';
  return { key, label: key, type, generic: true };
}
