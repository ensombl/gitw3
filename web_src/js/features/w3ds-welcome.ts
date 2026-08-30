import {GET} from '../modules/fetch.js';

type W3DSWelcomeVersion = {
  tag: string;
  version: string;
  ename: string;
  url: string;
};

type W3DSWelcomeStatus = {
  ready: boolean;
  status: string;
  ename: string;
  message: string;
  lastError?: string;
  versions: W3DSWelcomeVersion[];
};

function versionCard(version: W3DSWelcomeVersion, copyLabel: string) {
  const article = document.createElement('article');
  article.className = 'w3ds-welcome-version';

  const release = document.createElement('div');
  const link = document.createElement('a');
  link.href = version.url;
  const tag = document.createElement('strong');
  tag.textContent = version.tag;
  link.append(tag);
  const number = document.createElement('span');
  number.textContent = `Version ${version.version}`;
  release.append(link, number);

  const identity = document.createElement('div');
  identity.className = 'w3ds-welcome-version-ename';
  const code = document.createElement('code');
  code.textContent = version.ename;
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'ui button';
  copy.dataset.clipboardText = version.ename;
  copy.setAttribute('aria-label', copyLabel);
  copy.textContent = copyLabel;
  identity.append(code, copy);
  article.append(release, identity);
  return article;
}

function renderWelcome(root: HTMLElement, data: W3DSWelcomeStatus) {
  root.querySelector<HTMLElement>('[data-w3ds-welcome-pending]')?.classList.toggle('tw-hidden', data.ready);
  root.querySelector<HTMLElement>('[data-w3ds-welcome-identity]')?.classList.toggle('tw-hidden', !data.ready);
  root.querySelector<HTMLElement>('[data-w3ds-welcome-permanent]')?.classList.toggle('tw-hidden', !data.ready);
  const message = root.querySelector<HTMLElement>('[data-w3ds-welcome-message]');
  if (message) message.textContent = data.message;
  const eName = root.querySelector<HTMLElement>('[data-w3ds-welcome-ename]');
  if (eName) eName.textContent = data.ename;
  const copy = root.querySelector<HTMLElement>('[data-w3ds-welcome-copy]');
  if (copy) copy.dataset.clipboardText = data.ename;

  const error = root.querySelector<HTMLElement>('[data-w3ds-welcome-error]');
  error?.classList.toggle('tw-hidden', !data.lastError);
  const errorCode = error?.querySelector('code');
  if (errorCode) errorCode.textContent = data.lastError ?? '';

  const versions = root.querySelector<HTMLElement>('[data-w3ds-welcome-versions]');
  const empty = root.querySelector<HTMLElement>('[data-w3ds-welcome-empty]');
  const versionRecords = data.versions ?? [];
  empty?.classList.toggle('tw-hidden', versionRecords.length > 0 || !data.ready);
  if (versions) {
    const copyLabel = root.dataset.copyLabel ?? 'Copy';
    versions.replaceChildren(...versionRecords.map((version) => versionCard(version, copyLabel)));
  }
}

export function initW3DSWelcome() {
  const root = document.getElementById('w3ds-welcome-page');
  const statusUrl = root?.dataset.w3dsWelcomeStatusUrl;
  if (!root || !statusUrl) return;
  const interval = Number(root.dataset.w3dsWelcomePollInterval) || 2500;
  let stopped = false;

  const refresh = async () => {
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(`W3DS welcome status returned ${response.status}`);
      const data = await response.json() as W3DSWelcomeStatus;
      renderWelcome(root, data);
      stopped = data.ready;
    } catch {
      // Repository creation has completed even when W3DS is briefly
      // unreachable. Keep the hand-off screen open and try again.
    } finally {
      if (!stopped) window.setTimeout(refresh, interval);
    }
  };

  window.addEventListener('pagehide', () => { stopped = true }, {once: true});
  if (root.querySelector('[data-w3ds-welcome-pending]')?.classList.contains('tw-hidden')) return;
  window.setTimeout(refresh, interval);
}
