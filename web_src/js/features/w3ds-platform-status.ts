import {toCanvas} from 'qrcode';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';

type W3DSGuideStep = {
  ready: boolean;
  tone: 'green' | 'blue' | 'grey';
  label: string;
  message: string;
};

type PPAHistoryEvent = {
  kind: 'submission' | 'response' | 'decision';
  tone: 'info' | 'positive' | 'negative';
  title: string;
  message: string;
  actor?: string;
  createdAt: string;
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
  ppaActionMessage: string;
  ppaVersion: string;
  ppaLevel?: string;
  ppaDecidedAt?: string;
  ppaHistory: PPAHistoryEvent[];
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

function renderPPAHistory(root: HTMLElement, data: W3DSStatus) {
  const history = root.querySelector<HTMLElement>('[data-w3ds-ppa-history]');
  const list = history?.querySelector<HTMLOListElement>('[data-w3ds-ppa-history-list]');
  if (!history || !list) return;
  const events = data.ppaHistory ?? [];
  history.classList.toggle('tw-hidden', events.length === 0);
  const heading = history.querySelector<HTMLElement>('[data-w3ds-ppa-history-title]');
  if (heading) heading.textContent = (heading.dataset.titlePrefix ?? '').replace('%s', data.ppaVersion);
  list.replaceChildren(...events.map((event) => {
    const item = document.createElement('li');
    item.className = `w3ds-ppa-turn w3ds-ppa-turn-${event.kind}`;
    item.dataset.kind = event.kind;

    if (event.kind !== 'submission') {
      const avatar = document.createElement('span');
      avatar.className = 'w3ds-ppa-avatar';
      avatar.setAttribute('aria-hidden', 'true');
      avatar.textContent = event.kind === 'decision' ? 'PPA' : 'APP';
      item.append(avatar);
    }

    const bubble = document.createElement('article');
    bubble.className = `ui ${event.tone} message w3ds-ppa-bubble`;
    const heading = document.createElement('div');
    heading.className = 'w3ds-ppa-bubble-heading';
    const title = document.createElement('strong');
    title.className = 'header';
    title.textContent = event.title;
    heading.append(title);
    if (event.actor) {
      const actor = document.createElement('span');
      actor.className = 'w3ds-ppa-history-actor';
      actor.textContent = event.actor;
      heading.append(actor);
    }
    const message = document.createElement('p');
    message.className = 'w3ds-ppa-bubble-message';
    message.textContent = event.message;
    bubble.append(heading, message);
    if (event.createdAt) {
      const time = document.createElement('time');
      time.className = 'w3ds-ppa-decision-time';
      time.dateTime = event.createdAt;
      const timestamp = new Date(event.createdAt);
      const displayTime = Number.isNaN(timestamp.getTime()) ? event.createdAt : timestamp.toLocaleString();
      time.textContent = `${history.dataset.timePrefix ?? ''} ${displayTime}`.trim();
      bubble.append(time);
    }
    item.append(bubble);
    return item;
  }));
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
    ppaLabel.classList.remove('blue', 'green', 'red', 'yellow');
    ppaLabel.classList.add(data.ppaStatus === 'denied' ? 'red' : data.ppaStatus === 'granted' ? 'green' : data.ppaStatus === 'submitted' ? 'yellow' : 'blue');
  }
  const hasDecision = data.ppaStatus === 'denied' || data.ppaStatus === 'granted';
  root.querySelector<HTMLElement>('[data-w3ds-ppa-help]')?.classList.toggle('tw-hidden', hasDecision);
  root.querySelector<HTMLElement>('[data-w3ds-ppa-checklist]')?.classList.toggle('tw-hidden', hasDecision);
  renderPPAHistory(root, data);
  root.querySelector<HTMLElement>('[data-w3ds-ppa-action]')?.classList.toggle('tw-hidden', data.ppaStatus === 'granted');
  const responseField = root.querySelector<HTMLElement>('[data-w3ds-ppa-response]');
  const responseInput = responseField?.querySelector<HTMLTextAreaElement>('[data-w3ds-ppa-response-input]');
  responseField?.classList.toggle('tw-hidden', data.ppaStatus !== 'denied');
  if (responseInput) responseInput.required = data.ppaStatus === 'denied';
  const apply = root.querySelector<HTMLButtonElement>('[data-w3ds-ppa-apply]');
  if (apply) {
    apply.disabled = apply.dataset.canEdit !== 'true' || apply.dataset.signing === 'true' || !['ready', 'denied'].includes(data.ppaStatus);
    apply.classList.remove('primary', 'positive', 'negative');
    apply.classList.add(['submitted', 'granted'].includes(data.ppaStatus) ? 'positive' : 'primary');
  }
  const buttonLabel = root.querySelector<HTMLElement>('[data-w3ds-ppa-button-label]');
  if (buttonLabel) buttonLabel.textContent = data.ppaButton;
  const note = root.querySelector<HTMLElement>('[data-w3ds-ppa-note]');
  if (note && (!['ready', 'denied'].includes(data.ppaStatus) || apply?.dataset.canEdit === 'true')) note.textContent = data.ppaActionMessage;
}

export function renderW3DSTabStatus(tab: HTMLElement, data: W3DSStatus) {
  const dot = tab.querySelector<HTMLElement>('[data-w3ds-tab-status]');
  if (!dot) return;
  const visible = ['submitted', 'granted', 'denied'].includes(data.ppaStatus);
  dot.classList.toggle('tw-hidden', !visible);
  dot.classList.remove('submitted', 'granted', 'denied');
  if (visible) dot.classList.add(data.ppaStatus);
  dot.title = visible ? `${data.ppaLabel}: ${data.ppaMessage}` : '';
  dot.setAttribute('aria-label', dot.title);
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
    if (apply.disabled || !form.reportValidity()) return;

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
      const response = await POST(form.action, {data: new FormData(form), headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await responseMessage(response, signingStatus.dataset.failed ?? ''));
      const offer = await response.json() as PPASigningOffer;
      await toCanvas(canvas, offer.uri, {scale: 5, margin: 4, errorCorrectionLevel: 'M'});
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
  const tab = document.querySelector<HTMLElement>('[data-w3ds-tab]');
  const statusUrl = root?.dataset.w3dsStatusUrl || tab?.dataset.w3dsStatusUrl;
  if (!statusUrl || (!root && !tab)) return;

  const interval = Number(root?.dataset.w3dsPollInterval) || 15000;
  const checked = root?.querySelector<HTMLElement>('[data-w3ds-status-checked]');
  let stopped = false;

  if (root) initPPASigning(root);

  const refresh = async () => {
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (response.status === 404) {
        stopped = true;
        return;
      }
      if (!response.ok) throw new Error(`W3DS status returned ${response.status}`);
      const data = await response.json() as W3DSStatus;
      if (root) renderW3DSStatus(root, data);
      if (tab) renderW3DSTabStatus(tab, data);
      if (checked) checked.textContent = root?.dataset.w3dsCheckedText ?? '';
    } catch {
      if (checked) checked.textContent = root?.dataset.w3dsRefreshFailedText ?? '';
    } finally {
      if (!stopped) window.setTimeout(refresh, interval);
    }
  };

  window.addEventListener('pagehide', () => { stopped = true }, {once: true});
  window.setTimeout(refresh, interval);
}
