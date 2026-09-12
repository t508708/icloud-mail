import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const projectDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const localDir = path.join(projectDir, '.local');
const token = fs.readFileSync(path.join(localDir, 'cloudflare-token'), 'utf8').trim();
const domain = 'gooelv.com';
const webHost = 'icloud-us.gooelv.com';
const legacyWebHost = 'icloud.us.gooelv.com';
const imapHost = 'imap-icloud.us.gooelv.com';
const origin = '23.251.34.11';
const action = process.argv[2] || 'status';
const allowedActions = ['status', 'order-edge-cert', 'ensure-imap-dns', 'ensure-web-dns'];
if (!allowedActions.includes(action) || !token) {
  throw new Error('Usage: node scripts/cloudflare-public.mjs status|order-edge-cert|ensure-imap-dns|ensure-web-dns');
}

async function api(route, method = 'GET', body) {
  const response = await fetch('https://api.cloudflare.com/client/v4' + route, {
    method,
    headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(30000),
  });
  const data = await response.json();
  if (!response.ok || !data.success) {
    throw new Error(JSON.stringify({ route, status: response.status, errors: data.errors }));
  }
  return data.result;
}

function saveArtifact(name, value) {
  const directory = path.join(localDir, 'cloudflare');
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const filename = path.join(directory, `${Date.now()}-${name}.json`);
  fs.writeFileSync(filename, JSON.stringify(value, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
}

const zones = await api('/zones?name=' + domain);
if (zones.length !== 1 || zones[0].name !== domain) throw new Error('Expected exactly one matching zone');
const zonePath = '/zones/' + zones[0].id;
const certificateSummary = certificate => ({
  id: certificate.id, type: certificate.type, status: certificate.status,
  hosts: certificate.hosts, validation_errors: certificate.validation_errors,
});

if (action === 'status') {
  const packs = await api(zonePath + '/ssl/certificate_packs?status=all');
  console.log(JSON.stringify({ certificates: packs.map(certificateSummary) }));
  for (const hostname of [webHost, legacyWebHost, imapHost]) {
    const records = await api(zonePath + '/dns_records?name=' + hostname);
    console.log(JSON.stringify({ hostname, records: records.map(({ id, type, content, proxied }) => ({ id, type, content, proxied })) }));
  }
} else if (action === 'order-edge-cert') {
  const packs = await api(zonePath + '/ssl/certificate_packs?status=all');
  const existing = packs.find(pack => pack.hosts?.some(host => [legacyWebHost, '*.us.gooelv.com'].includes(host)) &&
    ['initializing', 'pending_validation', 'pending_issuance', 'pending_deployment', 'active'].includes(pack.status));
  if (existing) {
    console.log(JSON.stringify({ reused: true, ...certificateSummary(existing) }));
  } else {
    const request = {
      type: 'advanced', hosts: [domain, '*.us.gooelv.com'],
      certificate_authority: 'google', validation_method: 'txt', validity_days: 90,
    };
    saveArtifact('before-edge-order', { packs, request });
    // Certificate issuance uses existing zone entitlements; this script never creates subscriptions.
    const certificate = await api(zonePath + '/ssl/certificate_packs/order', 'POST', request);
    saveArtifact('edge-certificate', certificate);
    console.log(JSON.stringify(certificateSummary(certificate)));
  }
} else {
  const proxied = action === 'ensure-web-dns';
  const hostname = proxied ? webHost : imapHost;
  const records = await api(zonePath + '/dns_records?name=' + hostname);
  if (records.length) {
    if (records.length !== 1 || records[0].type !== 'A' || records[0].content !== origin || records[0].proxied !== proxied) {
      throw new Error('Existing DNS record differs from expected configuration');
    }
    console.log(JSON.stringify({ reused: true, id: records[0].id, hostname }));
  } else {
    saveArtifact('before-' + action, { hostname, records });
    const record = await api(zonePath + '/dns_records', 'POST', {
      type: 'A', name: hostname, content: origin, proxied, ttl: proxied ? 1 : 300,
      comment: proxied ? 'icloud-mail Web and API via Cloudflare' : 'icloud-mail IMAPS direct TLS endpoint',
    });
    saveArtifact(action + '-record', record);
    console.log(JSON.stringify({ id: record.id, hostname: record.name, content: record.content, proxied: record.proxied }));
  }
}
