import {toCanvas} from 'qrcode';
import {GET, POST} from '../modules/fetch.js';
import {showModal} from '../modules/modal.ts';

type SigningOffer = {uri: string; statusUrl: string};
type SigningStatus = {status: 'pending' | 'verifying' | 'review' | 'completed' | 'rejected' | 'expired'; redirect?: string; message?: string};

async function errorMessage(response: Response, fallback: string) {
  try {
    return (await response.json() as {message?: string}).message || fallback;
  } catch {
    return fallback;
  }
}

export function initW3DSPlatformPort() {
  const form = document.querySelector<HTMLFormElement>('[data-platform-port]');
  if (!form) return;
  const panels = Array.from(form.querySelectorAll<HTMLElement>('[data-platform-port-step]'));
  const indicators = Array.from(form.querySelectorAll<HTMLElement>('[data-platform-port-indicator]'));
  const back = form.querySelector<HTMLButtonElement>('[data-platform-port-back]');
  const next = form.querySelector<HTMLButtonElement>('[data-platform-port-next]');
  const submit = form.querySelector<HTMLButtonElement>('[data-platform-port-submit]');
  const dialog = document.getElementById('platform-port-signing-modal') as HTMLDialogElement | null;
  const canvas = dialog?.querySelector<HTMLCanvasElement>('[data-platform-port-qr]');
  const openWallet = dialog?.querySelector<HTMLAnchorElement>('[data-platform-port-open]');
  const status = dialog?.querySelector<HTMLElement>('[data-platform-port-status]');
  if (!back || !next || !submit || !dialog || !canvas || !openWallet || !status || panels.length !== 3) return;

  let step = 0;
  let stopped = true;
  const showStep = () => {
    for (let index = 0; index < panels.length; index++) {
      const active = index === step;
      panels[index].hidden = !active;
      panels[index].classList.toggle('tw-hidden', !active);
      indicators[index]?.classList.toggle('active', active);
      indicators[index]?.classList.toggle('complete', index < step);
    }
    back.hidden = step === 0;
    back.classList.toggle('tw-hidden', step === 0);
    next.hidden = step === panels.length - 1;
    next.classList.toggle('tw-hidden', step === panels.length - 1);
    submit.hidden = step !== panels.length - 1;
    submit.classList.toggle('tw-hidden', step !== panels.length - 1);
  };
  const validPanel = () => {
    for (const input of panels[step].querySelectorAll<HTMLInputElement>('input, textarea, select')) {
      if (!input.checkValidity()) {
        input.reportValidity();
        return false;
      }
    }
    return true;
  };
  back.addEventListener('click', () => {
    step = Math.max(0, step - 1);
    showStep();
  });
  next.addEventListener('click', () => {
    if (!validPanel()) return;
    step = Math.min(panels.length - 1, step + 1);
    showStep();
  });

  const setStatus = (message: string) => { status.textContent = message };
  const setLoading = (loading: boolean) => {
    submit.classList.toggle('loading', loading);
    submit.disabled = loading;
  };
  const poll = async (statusUrl: string) => {
    if (stopped) return;
    try {
      const response = await GET(statusUrl, {cache: 'no-store', headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await errorMessage(response, status.dataset.failed ?? ''));
      const result = await response.json() as SigningStatus;
      if (result.status === 'completed') {
        stopped = true;
        setStatus(status.dataset.complete ?? '');
        window.setTimeout(() => window.location.assign(result.redirect || '/'), 500);
        return;
      }
      if (result.status === 'review') {
        stopped = true;
        setLoading(false);
        setStatus(result.message || status.dataset.waiting || '');
        return;
      }
      if (result.status === 'rejected' || result.status === 'expired') {
        stopped = true;
        setLoading(false);
        setStatus(result.message || status.dataset.failed || '');
        return;
      }
    } catch (error) {
      stopped = true;
      setLoading(false);
      setStatus(error instanceof Error ? error.message : status.dataset.failed || '');
      return;
    }
    window.setTimeout(() => poll(statusUrl), 2000);
  };

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!form.reportValidity()) return;
    stopped = false;
    setLoading(true);
    setStatus(status.dataset.starting ?? '');
    canvas.width = 0;
    canvas.height = 0;
    openWallet.href = '#';
    showModal(dialog, () => {});
    dialog.addEventListener('close', () => {
      stopped = true;
      setLoading(false);
    }, {once: true});
    try {
      const response = await POST(form.action, {data: new FormData(form), headers: {accept: 'application/json'}});
      if (!response.ok) throw new Error(await errorMessage(response, status.dataset.failed ?? ''));
      const offer = await response.json() as SigningOffer;
      await toCanvas(canvas, offer.uri, {width: 380, margin: 4, errorCorrectionLevel: 'L'});
      openWallet.href = offer.uri;
      setStatus(status.dataset.waiting ?? '');
      poll(offer.statusUrl);
    } catch (error) {
      stopped = true;
      setLoading(false);
      setStatus(error instanceof Error ? error.message : status.dataset.failed || '');
    }
  });
  showStep();
}
