import {expect, test} from 'vitest';
import {renderW3DSStatus} from './w3ds-platform-status.ts';

test('renders a published W3DS status and guide', () => {
  document.body.innerHTML = `
    <main id="w3ds-platform-page">
      <div id="w3ds-publication-status" class="ui info message">
        <div data-w3ds-status-title></div><p data-w3ds-status-message></p>
        <p data-w3ds-status-error class="tw-hidden"><code></code></p>
      </div>
      <li data-w3ds-identity-step><span data-w3ds-step-label></span><p data-w3ds-step-message></p></li>
      <li data-w3ds-application-step><span data-w3ds-step-label></span><p data-w3ds-step-message></p></li>
      <li data-w3ds-release-step><span data-w3ds-step-label></span><p data-w3ds-step-message></p></li>
      <li data-w3ds-ppa-identity><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
      <li data-w3ds-ppa-application><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
      <li data-w3ds-ppa-domains><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
      <li data-w3ds-ppa-release><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
      <input data-w3ds-release-version readonly><a data-w3ds-release-link data-w3ds-release-create-url="/releases/new"></a>
      <span data-w3ds-ppa-label class="ui label tw-hidden"></span>
      <button data-w3ds-ppa-apply data-can-edit="true" disabled><span data-w3ds-ppa-button-label></span></button>
      <span data-w3ds-ppa-note></span>
    </main>`;
  const root = document.getElementById('w3ds-platform-page');

  renderW3DSStatus(root, {
    status: 'published',
    tone: 'positive',
    title: 'Platform published',
    message: 'Marketplace is synchronized.',
    ename: '@platform',
    isDraft: false,
    inSubmission: false,
    ppaStatus: 'ready',
    ppaLabel: 'Ready to apply',
    ppaMessage: 'Version 0.2.0 is ready to apply.',
    ppaButton: 'Apply for PPA certificate',
    ppaVersion: '0.2.0',
    releaseTag: 'v0.2.0',
    releaseUrl: '/releases/tag/v0.2.0',
    releaseAction: 'View release',
    identity: {ready: true, tone: 'green', label: 'Ready', message: 'Identity is @platform.'},
    application: {ready: true, tone: 'green', label: 'Ready', message: 'Application is deployed.'},
    domains: {ready: true, tone: 'green', label: 'Ready', message: 'Domains are selected.'},
    release: {ready: true, tone: 'green', label: 'Ready', message: 'Using v0.2.0.'},
  });

  expect(root.querySelector('#w3ds-publication-status')?.classList.contains('positive')).toBe(true);
  expect(root.querySelector('[data-w3ds-status-title]')?.textContent).toBe('Platform published');
  expect(root.querySelector('[data-w3ds-identity-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-application-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-release-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-ppa-application] svg:last-of-type')?.classList.contains('tw-hidden')).toBe(true);
  expect(root.querySelector('[data-w3ds-ppa-apply]')?.disabled).toBe(false);
  expect(root.querySelector('[data-w3ds-ppa-label]')?.textContent).toBe('Ready to apply');
  expect(root.querySelector('[data-w3ds-ppa-note]')?.textContent).toContain('0.2.0');
  expect(root.querySelector('[data-w3ds-release-version]')?.value).toBe('v0.2.0');
});

test('disables the version application when its eVault decision arrives', () => {
  document.body.innerHTML = `<main id="w3ds-platform-page">
    <div id="w3ds-publication-status" class="ui info message"><div data-w3ds-status-title></div><p data-w3ds-status-message></p></div>
    <span data-w3ds-ppa-label></span>
    <button data-w3ds-ppa-apply data-can-edit="true"><span data-w3ds-ppa-button-label></span></button>
    <span data-w3ds-ppa-note></span>
  </main>`;
  const root = document.getElementById('w3ds-platform-page');

  renderW3DSStatus(root, {
    status: 'published', tone: 'positive', title: 'Platform published', message: '', ename: '@platform',
    isDraft: false, inSubmission: false, ppaStatus: 'granted', ppaLabel: 'PPA certificate granted',
    ppaMessage: 'PPA granted L2 access for version 0.2.0.', ppaButton: 'PPA certificate granted',
    ppaVersion: '0.2.0', ppaLevel: 'L2',
    releaseTag: 'v0.2.0', releaseUrl: '/releases/tag/v0.2.0', releaseAction: 'View release',
    identity: {ready: true, tone: 'green', label: 'Ready', message: ''},
    application: {ready: true, tone: 'green', label: 'Ready', message: ''},
    domains: {ready: true, tone: 'green', label: 'Ready', message: ''},
    release: {ready: true, tone: 'green', label: 'Ready', message: ''},
  });

  expect(root.querySelector('[data-w3ds-ppa-apply]')?.disabled).toBe(true);
  expect(root.querySelector('[data-w3ds-ppa-apply]')?.classList.contains('positive')).toBe(true);
  expect(root.querySelector('[data-w3ds-ppa-button-label]')?.textContent).toBe('PPA certificate granted');
});
