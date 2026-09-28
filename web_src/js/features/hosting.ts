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
  tag: string;
  error?: string;
  warning?: string;
  url?: string;
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
};

function setHidden(element: Element | null | undefined, hidden: boolean) {
  if (!(element instanceof HTMLElement)) return;
  element.hidden = hidden;
  element.classList.toggle('tw-hidden', hidden);
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
  const statusText = root.querySelector<HTMLElement>('[data-managed-status]');
  const liveURL = root.querySelector<HTMLAnchorElement>('[data-managed-live-url]');
  const signingDialog = root.querySelector<HTMLDialogElement>('#managed-signing-modal');
  const signingCanvas = signingDialog?.querySelector<HTMLCanvasElement>('[data-managed-qr]');
  const openWallet = signingDialog?.querySelector<HTMLAnchorElement>('[data-managed-open-wallet]');
  let polling = 0;
  let shownSigning = '';

  const showSigning = async (signing: Signing) => {
    if (!signingDialog || !signingCanvas || !openWallet || shownSigning === signing.uri) return;
    shownSigning = signing.uri;
    await toCanvas(signingCanvas, signing.uri, {scale: 5, margin: 4, errorCorrectionLevel: 'L'});
    openWallet.href = signing.uri;
    if (!signingDialog.open) showModal(signingDialog, () => {});
  };

  const render = (result: StatusResponse) => {
    setHidden(progress, false);
    const current = stepForStatus[result.status];
    const currentIndex = stepOrder.indexOf(current);
    for (const step of root.querySelectorAll<HTMLElement>('[data-managed-step]')) {
      const index = stepOrder.indexOf(step.dataset.managedStep ?? '');
      step.classList.toggle('active', index === currentIndex && !result.final);
      step.classList.toggle('completed', index < currentIndex || result.status === 'live');
      step.classList.toggle('failed', result.final && result.status !== 'live' && index === Math.max(currentIndex, 0));
    }
    if (statusText) {
      statusText.textContent = [result.tag, result.label, result.error || result.warning].filter(Boolean).join(' · ');
    }
    if (liveURL) {
      setHidden(liveURL, !result.url);
      if (result.url) {
        liveURL.href = result.url;
        liveURL.textContent = result.url;
      }
    }
    if (result.signing) showSigning(result.signing);
    if (result.status !== 'awaiting_signature' && signingDialog?.open) signingDialog.close();
  };

  const pollOnce = async (statusURL: string, token: number) => {
    if (token !== polling) return;
    try {
      const response = await GET(statusURL, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await errorMessage(response));
      const result = await response.json() as StatusResponse;
      render(result);
      if (result.final) {
        window.setTimeout(() => window.location.reload(), result.status === 'live' ? 2500 : 1500);
        return;
      }
    } catch (error) {
      if (statusText) statusText.textContent = error instanceof Error ? error.message : String(error);
    }
    window.setTimeout(() => pollOnce(statusURL, token), 3000);
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
      if (result.signing) await showSigning(result.signing);
      poll(result.statusUrl);
    } catch (error) {
      setHidden(progress, false);
      if (statusText) statusText.textContent = error instanceof Error ? error.message : String(error);
      submit.disabled = false;
    } finally {
      submit.classList.remove('loading');
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

  const logDialog = root.querySelector<HTMLDialogElement>('#managed-log-modal');
  const logOutput = logDialog?.querySelector<HTMLElement>('[data-managed-log-output]');
  for (const button of root.querySelectorAll<HTMLButtonElement>('[data-managed-log]')) {
    button.addEventListener('click', async () => {
      if (!logDialog || !logOutput) return;
      logOutput.textContent = '…';
      showModal(logDialog, () => {});
      const response = await GET(button.dataset.managedLog ?? '', {cache: 'no-store'});
      logOutput.textContent = await response.text();
      logOutput.scrollTop = logOutput.scrollHeight;
    });
  }
}
