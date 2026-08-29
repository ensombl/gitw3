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
      <li data-w3ds-marketplace-step><span data-w3ds-step-label></span><p data-w3ds-step-message></p></li>
      <li data-w3ds-application-step><span data-w3ds-step-label></span><p data-w3ds-step-message></p></li>
      <li data-w3ds-ppa-identity><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
      <li data-w3ds-ppa-application><svg></svg><svg></svg><span data-w3ds-requirement-label></span></li>
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
    identity: {ready: true, tone: 'green', label: 'Ready', message: 'Identity is @platform.'},
    marketplace: {ready: true, tone: 'green', label: 'Ready', message: 'Listing is synchronized.'},
    application: {ready: true, tone: 'green', label: 'Ready', message: 'Application is deployed.'},
  });

  expect(root.querySelector('#w3ds-publication-status')?.classList.contains('positive')).toBe(true);
  expect(root.querySelector('[data-w3ds-status-title]')?.textContent).toBe('Platform published');
  expect(root.querySelector('[data-w3ds-identity-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-marketplace-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-application-step]')?.classList.contains('complete')).toBe(true);
  expect(root.querySelector('[data-w3ds-ppa-application] svg:last-of-type')?.classList.contains('tw-hidden')).toBe(true);
});
