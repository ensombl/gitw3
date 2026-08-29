import {toCanvas} from 'qrcode';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';

type W3DSGuideStep = {
  ready: boolean;
  tone: 'green' | 'blue' | 'grey';
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
  isDraft: boolean;
  inSubmission: boolean;
  ppaStatus: 'incomplete' | 'ready' | 'submitted' | 'granted' | 'denied';
  ppaLabel: string;
  ppaMessage: string;
  ppaButton: string;
  ppaVersion: string;
  ppaLevel?: string;
  releaseTag: string;
  releaseUrl: string;
  releaseAction: string;
  identity: W3DSGuideStep;
  application: W3DSGuideStep;
  domains: W3DSGuideStep;
  release: W3DSGuideStep;
};

type PPASigningOffer = {
  sessionId: string;
  uri: string;
  expiresAt: string;
  statusUrl: string;
};

type PPASigningStatus = {
  status: 'pending' | 'verifying' | 'completed' | 'rejected' | 'expired';
  redirect?: string;
  message?: string;
};

function renderGuideStep(element: HTMLElement | null, step: W3DSGuideStep) {
  if (!element) return;
  element.classList.toggle('complete', step.ready);
  const label = element.querySelector<HTMLElement>('[data-w3ds-step-label]');
  label?.classList.remove('green', 'blue', 'grey');
  label?.classList.add(step.tone);
  if (label) label.textContent = step.label;
  const message = element.querySelector<HTMLElement>('[data-w3ds-step-message]');
  if (message) message.textContent = step.message;
}

function renderRequirement(element: HTMLElement | null, step: W3DSGuideStep) {
  if (!element) return;
  const icons = element.querySelectorAll<SVGElement>('svg');
  icons[0]?.classList.toggle('tw-hidden', !step.ready);
  icons[1]?.classList.toggle('tw-hidden', step.ready);
  const label = element.querySelector<HTMLElement>('[data-w3ds-requirement-label]');
  label?.classList.remove('green', 'grey');
  label?.classList.add(step.ready ? 'green' : 'grey');
  if (label) label.textContent = step.label;
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
  renderGuideStep(root.querySelector('[data-w3ds-application-step]'), data.application);
  renderGuideStep(root.querySelector('[data-w3ds-release-step]'), data.release);
  renderRequirement(root.querySelector('[data-w3ds-ppa-identity]'), data.identity);
  renderRequirement(root.querySelector('[data-w3ds-ppa-application]'), data.application);
  renderRequirement(root.querySelector('[data-w3ds-ppa-domains]'), data.domains);
  renderRequirement(root.querySelector('[data-w3ds-ppa-release]'), data.release);

  const releaseVersion = root.querySelector<HTMLInputElement>('[data-w3ds-release-version]');
  if (releaseVersion) releaseVersion.value = data.releaseTag;
  const releaseLink = root.querySelector<HTMLAnchorElement>('[data-w3ds-release-link]');
  if (releaseLink) {
    releaseLink.href = data.releaseUrl || releaseLink.dataset.w3dsReleaseCreateUrl || '#';
    releaseLink.textContent = data.releaseAction;
  }

  const ppaLabel = root.querySelector<HTMLElement>('[data-w3ds-ppa-label]');
  if (ppaLabel) {
    ppaLabel.textContent = data.ppaLabel;
    ppaLabel.classList.toggle('tw-hidden', data.ppaStatus === 'incomplete');
    ppaLabel.classList.remove('blue', 'green', 'red');
    ppaLabel.classList.add(data.ppaStatus === 'denied' ? 'red' : data.ppaStatus === 'ready' ? 'blue' : 'green');
  }
  const apply = root.querySelector<HTMLButtonElement>('[data-w3ds-ppa-apply]');
  if (apply) {
    apply.disabled = apply.dataset.canEdit !== 'true' || apply.dataset.signing === 'true' || data.ppaStatus !== 'ready';
    apply.classList.remove('primary', 'positive', 'negative');
    apply.classList.add(data.ppaStatus === 'denied' ? 'negative' : ['submitted', 'granted'].includes(data.ppaStatus) ? 'positive' : 'primary');
  }
  const buttonLabel = root.querySelector<HTMLElement>('[data-w3ds-ppa-button-label]');
  if (buttonLabel) buttonLabel.textContent = data.ppaButton;
  const note = root.querySelector<HTMLElement>('[data-w3ds-ppa-note]');
  if (note && (data.ppaStatus !== 'ready' || apply?.dataset.canEdit === 'true')) note.textContent = data.ppaMessage;
}

async function responseMessage(response: Response, fallback: string) {
  try {
    const body = await response.json() as {message?: string};
    return body.message || fallback;
  } catch {
    return fallback;
  }
}

function initPPASigning(root: HTMLElement) {
  const form = root.querySelector<HTMLFormElement>('[data-w3ds-ppa-form]');
  const apply = form?.querySelector<HTMLButtonElement>('[data-w3ds-ppa-apply]');
  const dialog = document.getElementById('w3ds-ppa-signing-modal') as HTMLDialogElement | null;
  const canvas = dialog?.querySelector<HTMLCanvasElement>('[data-w3ds-ppa-qr]');
  const openWallet = dialog?.querySelector<HTMLAnchorElement>('[data-w3ds-ppa-open]');
  const signingStatus = dialog?.querySelector<HTMLElement>('[data-w3ds-ppa-signing-status]');
  if (!form || !apply || !dialog || !canvas || !openWallet || !signingStatus) return;

  let signingStopped = true;
  const setActive = (active: boolean) => {
    apply.dataset.signing = String(active);
    apply.classList.toggle('loading', active);
    apply.disabled = active || apply.dataset.canEdit !== 'true';
  };
  const setSigningStatus = (message: string) => {
    signingStatus.textContent = message;
  };

  const pollSigningStatus = async (statusUrl: string) => {
    if (signingStopped) return;
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await responseMessage(response, signingStatus.dataset.failed ?? ''));
      const result = await response.json() as PPASigningStatus;
      if (result.status === 'completed') {
        signingStopped = true;
        setSigningStatus(signingStatus.dataset.complete ?? '');
        window.setTimeout(() => window.location.assign(result.redirect || window.location.href), 700);
        return;
      }
      if (result.status === 'rejected' || result.status === 'expired') {
        signingStopped = true;
        setActive(false);
        setSigningStatus(result.message || signingStatus.dataset.failed || '');
        return;
      }
    } catch (error) {
      signingStopped = true;
      setActive(false);
      setSigningStatus(error instanceof Error ? error.message : signingStatus.dataset.failed || '');
      return;
    }
    window.setTimeout(() => pollSigningStatus(statusUrl), 2000);
  };

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (apply.disabled) return;

    signingStopped = false;
    setActive(true);
    setSigningStatus(signingStatus.dataset.starting ?? '');
    canvas.width = 0;
    canvas.height = 0;
    openWallet.href = '#';
    showModal(dialog, () => {});
    dialog.addEventListener('close', () => {
      signingStopped = true;
      setActive(false);
    }, {once: true});

    try {
      const response = await POST(form.action, {headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await responseMessage(response, signingStatus.dataset.failed ?? ''));
      const offer = await response.json() as PPASigningOffer;
      await toCanvas(canvas, offer.uri, {width: 260, margin: 1, errorCorrectionLevel: 'M'});
      openWallet.href = offer.uri;
      setSigningStatus(signingStatus.dataset.waiting ?? '');
      pollSigningStatus(offer.statusUrl);
    } catch (error) {
      signingStopped = true;
      setActive(false);
      setSigningStatus(error instanceof Error ? error.message : signingStatus.dataset.failed || '');
    }
  });
}

export function initW3DSPlatformStatus() {
  const root = document.getElementById('w3ds-platform-page');
  const statusUrl = root?.dataset.w3dsStatusUrl;
  if (!root || !statusUrl) return;

  const interval = Number(root.dataset.w3dsPollInterval) || 5000;
  const checked = root.querySelector<HTMLElement>('[data-w3ds-status-checked]');
  let stopped = false;

  initPPASigning(root);

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
