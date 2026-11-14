import {createHash, X509Certificate} from 'node:crypto';
import {readFileSync, statSync} from 'node:fs';
import {isIP} from 'node:net';

// Node loads this trust file at process startup. Chromium receives only its
// public-key pin, scoped to the disposable browser process. Validate the selected
// certificate against the requested host before launching that browser.
// No global certificate-verification override or private key is accepted.
export function navigationTransport(baseURL: string) {
  const url = new URL(baseURL);
  const args: string[] = [];
  const certificatePath = process.env.NODE_EXTRA_CA_CERTS;
  if (certificatePath) {
    if (statSync(certificatePath).size > 16384) throw new Error('invalid_fixture_certificate');
    const pem = readFileSync(certificatePath, 'utf8');
    if ((pem.match(/-----BEGIN CERTIFICATE-----/g) ?? []).length !== 1 || pem.includes('PRIVATE KEY')) throw new Error('invalid_fixture_certificate');
    const certificate = new X509Certificate(pem);
    const hostname = url.hostname.replace(/^\[|\]$/g, '');
    const matches = isIP(hostname) ? certificate.checkIP(hostname) : certificate.checkHost(hostname, {subject: 'never'});
    const now = Date.now();
    if (!matches || now < Date.parse(certificate.validFrom) || now > Date.parse(certificate.validTo)) throw new Error('invalid_fixture_certificate');
    const pin = createHash('sha256').update(certificate.publicKey.export({type:'spki',format:'der'})).digest('base64');
    args.push(`--ignore-certificate-errors-spki-list=${pin}`);
  }
  return {
    launch: {timeout:30_000, args},
    context: {baseURL, ignoreHTTPSErrors:false},
    api: {baseURL, ignoreHTTPSErrors:false, maxRedirects:0, timeout:30_000},
  };
}
