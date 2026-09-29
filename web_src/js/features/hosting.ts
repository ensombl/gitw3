import {toCanvas} from 'qrcode';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';

type Signing = {
  uri: string;
  deploymentEName: string;
  versionEName: string;
};

type DeployResponse = {
  id: number;
  statusUrl: string;
  message?: string;
  signing?: Signing;
};

type StatusResponse = {
  id: number;
  status: string;
  label: string;
  headline: string;
  detail: string;
  tag: string;
  error?: string;
  warning?: string;
  url?: string;
  logUrl?: string;
  startedUnix?: number;
  final: boolean;
  signing?: Signing;
};

const stepOrder = ['build', 'sign', 'rollout', 'live'];

const stepForStatus: Record<string, string> = {
  queued: 'build',
  building: 'build',
  built: 'build',
  awaiting_signature: 'sign',
  awaiting_certification: 'sign',
  deploying: 'rollout',
  live: 'live',
  build_failed: 'build',
  cancelled: 'build',
  superseded: 'build',
  deploy_failed: 'rollout',
  rolled_back: 'rollout',
};

// Rough share of the whole deploy each status represents, for the progress bar.
const percentForStatus: Record<string, number> = {
  queued: 6,
  building: 30,
  built: 50,
  awaiting_signature: 55,
  awaiting_certification: 55,
  deploying: 80,
  live: 100,
};

function setHidden(element: Element | null | undefined, hidden: boolean) {
  if (!(element instanceof HTMLElement)) return;
  element.hidden = hidden;
  element.classList.toggle('tw-hidden', hidden);
}

function formatElapsed(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, '0')}`;
}

async function errorMessage(response: Response) {
  try {
    return (await response.json() as {message?: string}).message || response.statusText;
  } catch {
    return response.statusText;
  }
}

export function initManagedDeploy() {
  const root = document.querySelector<HTMLElement>('#managed-deploy');
  if (!root) return;

  const progress = root.querySelector<HTMLElement>('[data-managed-progress]');
  const headline = progress?.querySelector<HTMLElement>('[data-managed-headline]');
  const statusText = progress?.querySelector<HTMLElement>('[data-managed-status]');
  const elapsed = progress?.querySelector<HTMLElement>('[data-managed-elapsed]');
  const bar = progress?.querySelector<HTMLElement>('[data-managed-bar]');
  const stepLog = progress?.querySelector<HTMLButtonElement>('[data-managed-step-log]');
  const signPanel = progress?.querySelector<HTMLElement>('[data-managed-sign]');
  const signingCanvas = signPanel?.querySelector<HTMLCanvasElement>('[data-managed-qr]');
  const openWallet = signPanel?.querySelector<HTMLAnchorElement>('[data-managed-open-wallet]');
  const done = progress?.querySelector<HTMLElement>('[data-managed-done]');
  const liveURL = done?.querySelector<HTMLAnchorElement>('[data-managed-live-url]');
  const liveOpen = done?.querySelector<HTMLAnchorElement>('[data-managed-live-open]');
  const logDialog = root.querySelector<HTMLDialogElement>('#managed-log-modal');
  const logOutput = logDialog?.querySelector<HTMLElement>('[data-managed-log-output]');
  const logTitle = logDialog?.querySelector<HTMLElement>('[data-managed-log-title]');
  const buildLogTitle = logTitle?.textContent ?? '';
  let polling = 0;
  let shownSigning = '';
  let startedAt = 0;
  let ticking = 0;

  let logRequest = 0;
  // Opens the log window; a live log (a running app) refreshes while it is
  // open and stays scrolled to the newest lines unless the reader scrolled up.
  const openLog = (url: string, title = buildLogTitle, live = false) => {
    if (!logDialog || !logOutput || !url) return;
    const request = ++logRequest;
    if (logTitle) logTitle.textContent = title;
    logOutput.textContent = '…';
    showModal(logDialog, () => {});
    const refresh = async (first: boolean) => {
      if (request !== logRequest || (!first && !logDialog.open)) return;
      const atBottom = first || logOutput.scrollTop + logOutput.clientHeight >= logOutput.scrollHeight - 8;
      try {
        const response = await GET(url, {cache: 'no-store'});
        const text = await response.text();
        if (request !== logRequest) return;
        logOutput.textContent = text;
      } catch (error) {
        logOutput.textContent = error instanceof Error ? error.message : String(error);
      }
      if (atBottom) logOutput.scrollTop = logOutput.scrollHeight;
      if (live) window.setTimeout(() => refresh(false), 4000);
    };
    refresh(true);
  };
  stepLog?.addEventListener('click', () => openLog(stepLog.dataset.url ?? ''));

  // The wallet approval is shown inline in the Sign step. It can be approved
  // while the build is still running, so it is not tied to a status.
  const showSigning = async (signing?: Signing) => {
    setHidden(signPanel, !signing);
    if (!signing || !signingCanvas || !openWallet || shownSigning === signing.uri) return;
    shownSigning = signing.uri;
    await toCanvas(signingCanvas, signing.uri, {scale: 4, margin: 2, errorCorrectionLevel: 'L'});
    openWallet.href = signing.uri;
  };

  const tick = () => {
    if (elapsed && startedAt) elapsed.textContent = formatElapsed(Math.max(0, Math.floor(Date.now() / 1000) - startedAt));
  };

  const setBusy = (busy: boolean) => {
    for (const form of root.querySelectorAll('[data-managed-deploy-form]')) setHidden(form, busy);
    window.clearInterval(ticking);
    if (busy) ticking = window.setInterval(tick, 1000);
  };

  const render = (result: StatusResponse) => {
    setHidden(progress, false);
    const live = result.status === 'live';
    const failed = result.final && !live;
    progress!.dataset.state = live ? 'done' : failed ? 'failed' : 'busy';
    setBusy(!result.final);
    if (result.startedUnix) startedAt = result.startedUnix;
    tick();

    const currentIndex = stepOrder.indexOf(stepForStatus[result.status] ?? 'build');
    for (const step of progress!.querySelectorAll<HTMLElement>('[data-managed-step]')) {
      const index = stepOrder.indexOf(step.dataset.managedStep ?? '');
      const signingNow = step.dataset.managedStep === 'sign' && Boolean(result.signing);
      step.classList.toggle('active', !result.final && (index === currentIndex || signingNow));
      step.classList.toggle('completed', live || (index < currentIndex && !signingNow));
      step.classList.toggle('failed', failed && index === currentIndex);
    }
    if (bar) {
      const percent = live ? 100 : percentForStatus[result.status] ?? Number.parseFloat(bar.style.width || '0');
      bar.style.width = `${percent}%`;
      bar.parentElement?.setAttribute('aria-valuenow', String(Math.round(percent)));
    }
    if (headline && (result.headline || result.label)) headline.textContent = result.headline || result.label;
    if (statusText) {
      statusText.textContent = [result.tag, result.error || result.warning || result.detail].filter(Boolean).join(' · ');
    }
    if (stepLog) {
      stepLog.dataset.url = result.logUrl ?? '';
      setHidden(stepLog, !result.logUrl || result.status === 'queued');
    }
    showSigning(result.signing);
    setHidden(done, !(live && result.url));
    if (live && result.url && liveURL && liveOpen) {
      liveURL.href = result.url;
      liveURL.textContent = result.url.replace(/^https?:\/\//, '');
      liveOpen.href = result.url;
      for (const badge of root.querySelectorAll('[data-managed-offline]')) setHidden(badge, true);
    }
  };

  const pollOnce = async (statusURL: string, token: number) => {
    if (token !== polling) return;
    try {
      const response = await GET(statusURL, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await errorMessage(response));
      const result = await response.json() as StatusResponse;
      render(result);
      if (result.final) {
        // A failure is explained by the failure panel the page renders.
        if (result.status !== 'live') window.setTimeout(() => window.location.reload(), 2000);
        return;
      }
    } catch (error) {
      if (statusText) statusText.textContent = error instanceof Error ? error.message : String(error);
    }
    window.setTimeout(() => pollOnce(statusURL, token), 2500);
  };
  const poll = (statusURL: string) => pollOnce(statusURL, ++polling);

  const form = root.querySelector<HTMLFormElement>('[data-managed-deploy-form]');
  const submit = form?.querySelector<HTMLButtonElement>('[data-managed-deploy-submit]');
  form?.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!form.reportValidity() || !submit) return;
    submit.disabled = true;
    submit.classList.add('loading');
    try {
      const response = await POST(form.action, {data: new FormData(form), headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await errorMessage(response));
      const result = await response.json() as DeployResponse;
      startedAt = Math.floor(Date.now() / 1000);
      render({id: result.id, status: 'queued', label: '', headline: '', detail: '', tag: '', final: false, signing: result.signing});
      poll(result.statusUrl);
    } catch (error) {
      setHidden(progress, false);
      if (progress) progress.dataset.state = 'failed';
      if (statusText) statusText.textContent = error instanceof Error ? error.message : String(error);
      submit.disabled = false;
    } finally {
      submit.classList.remove('loading');
      submit.disabled = false;
    }
  });

  // Resume progress for a deployment started earlier (or by a release).
  const pending = root.querySelector<HTMLElement>('[data-managed-pending]');
  if (pending?.dataset.managedPending) poll(pending.dataset.managedPending);

  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-managed-action]')) {
    button.addEventListener('click', async () => {
      if (button.dataset.confirm && !window.confirm(button.dataset.confirm)) return;
      button.classList.add('loading');
      const data = new FormData();
      if (button.dataset.key) data.set('key', button.dataset.key);
      try {
        const response = await POST(button.dataset.managedAction ?? '', {data, headers: {accept: 'application/json'}});
        if (!response.ok) throw new Error(await errorMessage(response));
        const result = await response.json() as {redirect?: string; statusUrl?: string};
        if (result.statusUrl) {
          poll(result.statusUrl);
          return;
        }
        window.location.assign(result.redirect || window.location.href);
      } catch (error) {
        window.alert(error instanceof Error ? error.message : String(error));
      } finally {
        button.classList.remove('loading');
      }
    });
  }

  for (const inlineForm of root.querySelectorAll<HTMLFormElement>('[data-managed-form]')) {
    inlineForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      const button = inlineForm.querySelector<HTMLButtonElement>('button');
      button?.classList.add('loading');
      try {
        const response = await POST(inlineForm.action, {data: new FormData(inlineForm), headers: {accept: 'application/json'}});
        if (!response.ok) throw new Error(await errorMessage(response));
        window.location.reload();
      } catch (error) {
        window.alert(error instanceof Error ? error.message : String(error));
      } finally {
        button?.classList.remove('loading');
      }
    });
  }

  for (const toggle of root.querySelectorAll<HTMLInputElement>('[data-managed-auto-deploy]')) {
    toggle.addEventListener('change', async () => {
      const data = new FormData();
      data.set('enabled', String(toggle.checked));
      const response = await POST(toggle.dataset.managedAutoDeploy ?? '', {data, headers: {accept: 'application/json'}});
      if (!response.ok) {
        toggle.checked = !toggle.checked;
        window.alert(await errorMessage(response));
      }
    });
  }

  // Live availability check for the address field.
  for (const field of root.querySelectorAll<HTMLElement>('[data-managed-subdomain]')) {
    const input = field.querySelector<HTMLInputElement>('input');
    const status = field.nextElementSibling as HTMLElement | null;
    if (!input || !status?.hasAttribute('data-managed-subdomain-status')) continue;
    let timer = 0;
    let sequence = 0;
    const check = async () => {
      const value = input.value.trim().toLowerCase();
      input.value = value;
      if (!value) {
        status.textContent = '';
        return;
      }
      const current = ++sequence;
      const params = new URLSearchParams({name: value, target: field.dataset.target ?? ''});
      try {
        const response = await GET(`${field.dataset.checkUrl}?${params}`, {headers: {accept: 'application/json'}});
        const result = await response.json() as {available: boolean; url?: string; message?: string};
        if (current !== sequence) return;
        status.classList.toggle('available', result.available);
        status.classList.toggle('taken', !result.available);
        status.textContent = result.available ? `✓ ${result.url}` : `✗ ${result.message ?? ''}`;
        input.setCustomValidity(result.available ? '' : result.message ?? 'unavailable');
      } catch {
        status.textContent = '';
      }
    };
    input.addEventListener('input', () => {
      window.clearTimeout(timer);
      input.setCustomValidity('');
      timer = window.setTimeout(check, 300);
    });
    check();
  }

  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-managed-log]')) {
    button.addEventListener('click', () => openLog(button.dataset.managedLog ?? '',
      button.dataset.managedLogTitle || buildLogTitle, button.hasAttribute('data-managed-log-live')));
  }
}
