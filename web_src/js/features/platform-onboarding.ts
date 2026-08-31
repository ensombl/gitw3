import {p256} from '@noble/curves/nist.js';

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function hexToBytes(hex: string): Uint8Array {
  return Uint8Array.from(hex.match(/.{2}/g) ?? [], (byte) => Number.parseInt(byte, 16));
}

function concatBytes(...arrays: Uint8Array[]): Uint8Array {
  const result = new Uint8Array(arrays.reduce((length, array) => length + array.length, 0));
  let offset = 0;
  for (const array of arrays) {
    result.set(array, offset);
    offset += array.length;
  }
  return result;
}

export function bytesToBase58(bytes: Uint8Array): string {
  if (bytes.length === 0) return '';
  const alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
  const digits = [0];
  for (const byte of bytes) {
    let carry = byte;
    for (let index = 0; index < digits.length; index++) {
      carry += digits[index] << 8;
      digits[index] = carry % 58;
      carry = Math.floor(carry / 58);
    }
    while (carry > 0) {
      digits.push(carry % 58);
      carry = Math.floor(carry / 58);
    }
  }
  for (let index = 0; bytes[index] === 0 && index < bytes.length - 1; index++) digits.push(0);
  return digits.reverse().map((digit) => alphabet[digit]).join('');
}

function exportP256Spki(publicKey: Uint8Array): Uint8Array {
  const header = hexToBytes('3059301306072a8648ce3d020106082a8648ce3d030107034200');
  return concatBytes(header, publicKey);
}

function exportP256Pkcs8(secretKey: Uint8Array, publicKey: Uint8Array): Uint8Array {
  const header = hexToBytes('308187020100301306072a8648ce3d020106082a8648ce3d030107046d306b0201010420');
  const publicKeyHeader = hexToBytes('a144034200');
  return concatBytes(header, secretKey, publicKeyHeader, publicKey);
}

export async function generatePlatformKeyPair(subtle: SubtleCrypto | null | undefined = globalThis.crypto?.subtle) {
  if (subtle) {
    const pair = await subtle.generateKey({name: 'ECDSA', namedCurve: 'P-256'}, true, ['sign', 'verify']);
    const [spki, pkcs8] = await Promise.all([
      subtle.exportKey('spki', pair.publicKey),
      subtle.exportKey('pkcs8', pair.privateKey),
    ]);
    return {publicKeySpki: new Uint8Array(spki), privateKeyPkcs8: new Uint8Array(pkcs8)};
  }

  const {secretKey} = p256.keygen();
  const publicKey = p256.getPublicKey(secretKey, false);
  return {
    publicKeySpki: exportP256Spki(publicKey),
    privateKeyPkcs8: exportP256Pkcs8(secretKey, publicKey),
  };
}

export function downloadKeyBackup(publicKey: string, privateKey: Uint8Array, format = 'w3ds-platform-key-v1', filename = 'w3ds-platform-key.json') {
  const backup = {
    format,
    algorithm: {name: 'ECDSA', namedCurve: 'P-256', hash: 'SHA-256'},
    publicKey,
    privateKeyPkcs8: bytesToBase64(privateKey),
    createdAt: new Date().toISOString(),
    warning: 'Keep this private key secret. GitW3 cannot recover it.',
  };
  const blob = new Blob([`${JSON.stringify(backup, null, 2)}\n`], {type: 'application/json'});
  const link = document.createElement('a');
  link.href = URL.createObjectURL(blob);
  link.download = filename;
  document.body.append(link);
  link.click();
  setTimeout(() => {
    URL.revokeObjectURL(link.href);
    link.remove();
  });
}

export function initPlatformOnboarding() {
  const form = document.querySelector<HTMLFormElement>('#platform-onboarding-form');
  if (!form) return;

  const steps = Array.from(form.querySelectorAll<HTMLElement>('[data-platform-step]'));
  const indicators = Array.from(form.querySelectorAll<HTMLElement>('[data-platform-step-indicator]'));
  const back = form.querySelector<HTMLButtonElement>('#platform-step-back')!;
  const next = form.querySelector<HTMLButtonElement>('#platform-step-next')!;
  const submit = form.querySelector<HTMLButtonElement>('#platform-create-submit')!;
  let current = 0;

  const setHidden = (element: HTMLElement, hidden: boolean) => {
    element.hidden = hidden;
    element.classList.toggle('tw-hidden', hidden);
  };

  const showStep = () => {
    for (const [index, step] of steps.entries()) setHidden(step, index !== current);
    for (const [index, indicator] of indicators.entries()) {
      indicator.classList.toggle('active', index === current);
      indicator.classList.toggle('completed', index < current);
    }
    setHidden(back, current === 0);
    setHidden(next, current === steps.length - 1);
    setHidden(submit, current !== steps.length - 1);
  };

  const currentStepIsValid = () => {
    const controls = steps[current].querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>('input, textarea, select');
    for (const control of controls) {
      if (!control.checkValidity()) {
        control.reportValidity();
        return false;
      }
    }
    if (current === 1) {
      const domains = Array.from(form.querySelectorAll<HTMLInputElement>('input[name="platform_domains"]'));
      if (domains.length > 0 && !domains.some((domain) => domain.checked)) {
        domains[0].setCustomValidity(form.dataset.platformDomainsRequiredText ?? 'Select at least one application domain.');
        domains[0].reportValidity();
        domains[0].setCustomValidity('');
        return false;
      }
    }
    return true;
  };

  next.addEventListener('click', () => {
    if (!currentStepIsValid()) return;
    current += 1;
    showStep();
  });
  back.addEventListener('click', () => {
    current -= 1;
    showStep();
  });

  const aiInstall = form.querySelector<HTMLElement>('[data-platform-ai-install]')!;
  for (const radio of form.querySelectorAll<HTMLInputElement>('input[name="use_ai_tooling"]')) {
    radio.addEventListener('change', () => setHidden(aiInstall, radio.checked && radio.value === 'false'));
  }

  showStep();
}
