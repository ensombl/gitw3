function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function downloadKeyBackup(publicKey: string, privateKey: ArrayBuffer) {
  const backup = {
    format: 'w3ds-platform-key-v1',
    algorithm: {name: 'ECDSA', namedCurve: 'P-256', hash: 'SHA-256'},
    publicKey,
    privateKeyPkcs8: bytesToBase64(new Uint8Array(privateKey)),
    createdAt: new Date().toISOString(),
    warning: 'Keep this private key secret. GitW3 cannot recover it.',
  };
  const blob = new Blob([`${JSON.stringify(backup, null, 2)}\n`], {type: 'application/json'});
  const link = document.createElement('a');
  link.href = URL.createObjectURL(blob);
  link.download = 'w3ds-platform-key.json';
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

  const showStep = () => {
    for (const [index, step] of steps.entries()) step.hidden = index !== current;
    for (const [index, indicator] of indicators.entries()) {
      indicator.classList.toggle('active', index === current);
      indicator.classList.toggle('completed', index < current);
    }
    back.hidden = current === 0;
    next.hidden = current === steps.length - 1;
    submit.hidden = current !== steps.length - 1;
  };

  const currentStepIsValid = () => {
    const controls = steps[current].querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>('input, textarea, select');
    for (const control of controls) {
      if (!control.checkValidity()) {
        control.reportValidity();
        return false;
      }
    }
    if (current === 2) {
      const mode = form.querySelector<HTMLInputElement>('input[name="platform_key_mode"]:checked')?.value;
      const publicKey = form.querySelector<HTMLInputElement>('#platform-public-key')!;
      const saved = form.querySelector<HTMLInputElement>('#platform-key-saved')!;
      if (mode === 'generate' && (!publicKey.value || !saved.checked)) {
        saved.setCustomValidity('Generate, download, and save the platform key before continuing.');
        saved.reportValidity();
        saved.setCustomValidity('');
        return false;
      }
      if (mode === 'paste') {
        const pasted = form.querySelector<HTMLTextAreaElement>('#platform-public-key-paste')!;
        if (!pasted.value.trim().startsWith('z')) {
          pasted.setCustomValidity('Enter a z-prefixed multibase public key.');
          pasted.reportValidity();
          pasted.setCustomValidity('');
          return false;
        }
        publicKey.value = pasted.value.trim();
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

  const repoName = form.querySelector<HTMLInputElement>('#platform-repo-name')!;
  const platformName = form.querySelector<HTMLInputElement>('#platform-name')!;
  repoName.addEventListener('input', () => {
    if (platformName.dataset.edited === 'true') return;
    platformName.value = repoName.value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  });
  platformName.addEventListener('input', () => platformName.dataset.edited = 'true');

  const generated = form.querySelector<HTMLElement>('[data-platform-generated-key]')!;
  const pasted = form.querySelector<HTMLElement>('[data-platform-pasted-key]')!;
  for (const radio of form.querySelectorAll<HTMLInputElement>('input[name="platform_key_mode"]')) {
    radio.addEventListener('change', () => {
      const generate = radio.checked && radio.value === 'generate';
      generated.hidden = !generate;
      pasted.hidden = generate;
    });
  }

  form.querySelector<HTMLButtonElement>('#platform-generate-key')!.addEventListener('click', async (event) => {
    const button = event.currentTarget as HTMLButtonElement;
    button.disabled = true;
    try {
      const pair = await crypto.subtle.generateKey({name: 'ECDSA', namedCurve: 'P-256'}, true, ['sign', 'verify']);
      const [spki, pkcs8] = await Promise.all([
        crypto.subtle.exportKey('spki', pair.publicKey),
        crypto.subtle.exportKey('pkcs8', pair.privateKey),
      ]);
      const publicKey = `z${bytesToHex(new Uint8Array(spki))}`;
      form.querySelector<HTMLInputElement>('#platform-public-key')!.value = publicKey;
      downloadKeyBackup(publicKey, pkcs8);
    } finally {
      button.disabled = false;
    }
  });

  const aiInstall = form.querySelector<HTMLElement>('[data-platform-ai-install]')!;
  for (const radio of form.querySelectorAll<HTMLInputElement>('input[name="use_ai_tooling"]')) {
    radio.addEventListener('change', () => aiInstall.hidden = radio.checked && radio.value === 'false');
  }

  showStep();
}
