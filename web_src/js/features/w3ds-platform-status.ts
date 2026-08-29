import {GET} from '../modules/fetch.js';

type W3DSGuideStep = {
  ready: boolean;
  label: string;
  message: string;
};

export type W3DSStatus = {
  status: string;
  tone: 'positive' | 'warning' | 'info';
  title: string;
  message: string;
  ename: string;
  lastError?: string;
  identity: W3DSGuideStep;
  marketplace: W3DSGuideStep;
};

function renderGuideStep(element: HTMLElement | null, step: W3DSGuideStep) {
  if (!element) return;
  element.classList.toggle('complete', step.ready);
  const label = element.querySelector<HTMLElement>('[data-w3ds-step-label]');
  label?.classList.remove('green', 'blue');
  label?.classList.add(step.ready ? 'green' : 'blue');
  if (label) label.textContent = step.label;
  const message = element.querySelector<HTMLElement>('[data-w3ds-step-message]');
  if (message) message.textContent = step.message;
}

export function renderW3DSStatus(root: HTMLElement, data: W3DSStatus) {
  const status = root.querySelector<HTMLElement>('#w3ds-publication-status');
  if (!status) return;
  status.dataset.status = data.status;
  status.classList.remove('positive', 'warning', 'info');
  status.classList.add(data.tone);

  const title = status.querySelector<HTMLElement>('[data-w3ds-status-title]');
  if (title) title.textContent = data.title;
  const message = status.querySelector<HTMLElement>('[data-w3ds-status-message]');
  if (message) message.textContent = data.message;
  const error = status.querySelector<HTMLElement>('[data-w3ds-status-error]');
  if (error) {
    error.classList.toggle('tw-hidden', !data.lastError);
    const code = error.querySelector('code');
    if (code) code.textContent = data.lastError ?? '';
  }

  renderGuideStep(root.querySelector('[data-w3ds-identity-step]'), data.identity);
  renderGuideStep(root.querySelector('[data-w3ds-marketplace-step]'), data.marketplace);
}

export function initW3DSPlatformStatus() {
  const root = document.getElementById('w3ds-platform-page');
  const statusUrl = root?.dataset.w3dsStatusUrl;
  if (!root || !statusUrl) return;

  const interval = Number(root.dataset.w3dsPollInterval) || 5000;
  const checked = root.querySelector<HTMLElement>('[data-w3ds-status-checked]');
  let stopped = false;

  const refresh = async () => {
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(`W3DS status returned ${response.status}`);
      renderW3DSStatus(root, await response.json());
      if (checked) checked.textContent = root.dataset.w3dsCheckedText ?? '';
    } catch {
      if (checked) checked.textContent = root.dataset.w3dsRefreshFailedText ?? '';
    } finally {
      if (!stopped) window.setTimeout(refresh, interval);
    }
  };

  window.addEventListener('pagehide', () => { stopped = true }, {once: true});
  window.setTimeout(refresh, interval);
}
