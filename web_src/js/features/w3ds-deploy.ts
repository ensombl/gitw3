import {toCanvas} from 'qrcode';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';
import {bytesToBase58, downloadKeyBackup, generatePlatformKeyPair} from './platform-onboarding.ts';

type DeploymentOffer = {
  id: string;
  uri: string;
  statusUrl: string;
  deploymentEName: string;
  versionEName: string;
};

type DeploymentStatus = {
  status: string;
  message?: string;
  redirect?: string;
};

function setHidden(element: HTMLElement | null, hidden: boolean) {
  if (!element) return;
  element.hidden = hidden;
  element.classList.toggle('tw-hidden', hidden);
}

async function responseMessage(response: Response, fallback: string) {
  try {
    return (await response.json() as {message?: string}).message || fallback;
  } catch {
    return fallback;
  }
}

export function initW3DSDeploy() {
  const root = document.getElementById('w3ds-deploy-page');
  const form = root?.querySelector<HTMLFormElement>('[data-deployment-form]');
  if (!root || !form) return;

  const environment = form.querySelector<HTMLSelectElement>('#deployment_environment');
  const customEnvironment = form.querySelector<HTMLElement>('[data-custom-environment]');
  const customEnvironmentInput = form.querySelector<HTMLInputElement>('#deployment_custom_environment');
  environment?.addEventListener('change', () => {
    const custom = environment.value === 'custom';
    setHidden(customEnvironment, !custom);
    if (customEnvironmentInput) customEnvironmentInput.required = custom;
  });

  const generated = form.querySelector<HTMLElement>('[data-generated-deployment-key]');
  const pasted = form.querySelector<HTMLElement>('[data-pasted-deployment-key]');
  const publicKey = form.querySelector<HTMLInputElement>('[data-deployment-public-key]');
  const pastedKey = form.querySelector<HTMLTextAreaElement>('#deployment_public_key_paste');
  const saved = form.querySelector<HTMLInputElement>('[data-deployment-key-saved]');
  for (const radio of form.querySelectorAll<HTMLInputElement>('input[name="deployment_key_mode"]')) {
    radio.addEventListener('change', () => {
      const generate = radio.checked && radio.value === 'generate';
      setHidden(generated, !generate);
      setHidden(pasted, generate);
      if (saved) saved.required = generate;
      if (pastedKey) pastedKey.required = !generate;
      if (publicKey) publicKey.value = '';
    });
  }

  form.querySelector<HTMLButtonElement>('[data-generate-deployment-key]')?.addEventListener('click', async (event) => {
    const button = event.currentTarget as HTMLButtonElement;
    button.disabled = true;
    try {
      const {publicKeySpki, privateKeyPkcs8} = await generatePlatformKeyPair();
      const encoded = `z${bytesToBase58(publicKeySpki)}`;
      if (publicKey) publicKey.value = encoded;
      if (saved) saved.checked = false;
      downloadKeyBackup(encoded, privateKeyPkcs8, 'w3ds-deployment-key-v1', 'w3ds-deployment-key.json');
    } finally {
      button.disabled = false;
    }
  });

  const submit = form.querySelector<HTMLButtonElement>('[data-create-deployment]');
  const dialog = document.getElementById('w3ds-deployment-signing-modal') as HTMLDialogElement | null;
  const canvas = dialog?.querySelector<HTMLCanvasElement>('[data-deployment-qr]');
  const openWallet = dialog?.querySelector<HTMLAnchorElement>('[data-deployment-open-wallet]');
  const status = dialog?.querySelector<HTMLElement>('[data-deployment-signing-status]');
  const deploymentEName = dialog?.querySelector<HTMLElement>('[data-preview-deployment-ename]');
  const versionEName = dialog?.querySelector<HTMLElement>('[data-preview-version-ename]');
  if (!submit || !dialog || !canvas || !openWallet || !status) return;

  let stopped = true;
  const setBusy = (busy: boolean) => {
    submit.disabled = busy;
    submit.classList.toggle('loading', busy);
  };
  const poll = async (statusUrl: string) => {
    if (stopped) return;
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await responseMessage(response, status.dataset.failed ?? 'Deployment failed.'));
      const result = await response.json() as DeploymentStatus;
      if (result.status === 'completed') {
        stopped = true;
        status.textContent = status.dataset.complete ?? 'Deployment published.';
        window.setTimeout(() => window.location.assign(result.redirect || window.location.href), 700);
        return;
      }
      if (result.status === 'failed' || result.status === 'expired') {
        stopped = true;
        setBusy(false);
        status.textContent = result.message || status.dataset.failed || 'Deployment failed.';
        return;
      }
      status.textContent = result.status === 'publishing' ? 'Signature accepted. Publishing the deployment records…' : status.dataset.waiting || '';
    } catch (error) {
      stopped = true;
      setBusy(false);
      status.textContent = error instanceof Error ? error.message : status.dataset.failed || '';
      return;
    }
    window.setTimeout(() => poll(statusUrl), 2000);
  };

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const mode = form.querySelector<HTMLInputElement>('input[name="deployment_key_mode"]:checked')?.value;
    if (mode === 'generate') {
      if (!publicKey?.value || !saved?.checked) {
        saved?.setCustomValidity('Generate, download, and confirm that you saved the private key.');
        saved?.reportValidity();
        saved?.setCustomValidity('');
        return;
      }
    } else {
      const value = pastedKey?.value.trim() || '';
      if (!value.startsWith('z')) {
        pastedKey?.setCustomValidity('Enter a z-prefixed W3DS public key.');
        pastedKey?.reportValidity();
        pastedKey?.setCustomValidity('');
        return;
      }
      if (publicKey) publicKey.value = value;
    }
    if (!form.reportValidity()) return;

    stopped = false;
    setBusy(true);
    status.textContent = status.dataset.starting ?? '';
    canvas.width = 0;
    canvas.height = 0;
    openWallet.href = '#';
    if (deploymentEName) deploymentEName.textContent = '';
    if (versionEName) versionEName.textContent = '';
    showModal(dialog, () => {});
    dialog.addEventListener('close', () => {
      stopped = true;
      setBusy(false);
    }, {once: true});

    try {
      const response = await POST(form.action, {data: new FormData(form), headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await responseMessage(response, status.dataset.failed ?? ''));
      const offer = await response.json() as DeploymentOffer;
      if (deploymentEName) deploymentEName.textContent = offer.deploymentEName;
      if (versionEName) versionEName.textContent = offer.versionEName;
      await toCanvas(canvas, offer.uri, {scale: 8, margin: 4, errorCorrectionLevel: 'L'});
      openWallet.href = offer.uri;
      status.textContent = status.dataset.waiting ?? '';
      poll(offer.statusUrl);
    } catch (error) {
      stopped = true;
      setBusy(false);
      status.textContent = error instanceof Error ? error.message : status.dataset.failed || '';
    }
  });
}
