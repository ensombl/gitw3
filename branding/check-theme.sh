#!/bin/sh
# Verify that GitW3's primary palette is part of the theme itself, exactly once.
set -eu

if [ "$#" -eq 0 ]; then
    set -- \
        web_src/css/themes/theme-forgejo-light.css \
        web_src/css/themes/theme-forgejo-dark.css
fi

node --input-type=module - "$@" <<'JS'
import fs from 'node:fs';
import path from 'node:path';

const palettes = {
  light: {
    'color-primary': '#5b34d1',
    'color-primary-contrast': '#ffffff',
    'color-primary-dark-1': '#5b34d1',
    'color-primary-dark-2': '#4c2ab0',
    'color-primary-dark-3': '#4c2ab0',
    'color-primary-dark-4': '#3d2190',
    'color-primary-dark-5': '#3d2190',
    'color-primary-dark-6': '#3d2190',
    'color-primary-dark-7': '#2e1a6b',
    'color-primary-light-1': '#6b46e5',
    'color-primary-light-2': '#7a55f5',
    'color-primary-light-3': '#8968ff',
    'color-primary-light-4': '#a186ff',
    'color-primary-light-5': '#b8a4ff',
    'color-primary-light-6': '#e7e1ff',
    'color-primary-light-7': '#f3f0ff',
    'color-primary-alpha-10': '#5b34d119',
    'color-primary-alpha-20': '#5b34d133',
    'color-primary-alpha-30': '#5b34d14b',
    'color-primary-alpha-40': '#5b34d166',
    'color-primary-alpha-50': '#5b34d180',
    'color-primary-alpha-60': '#5b34d199',
    'color-primary-alpha-70': '#5b34d1b3',
    'color-primary-alpha-80': '#5b34d1cc',
    'color-primary-alpha-90': '#5b34d1e1',
  },
  dark: {
    'color-primary': '#a186ff',
    'color-primary-contrast': '#1d2636',
    'color-primary-dark-1': '#b8a4ff',
    'color-primary-dark-2': '#b8a4ff',
    'color-primary-dark-3': '#d0c3ff',
    'color-primary-dark-4': '#d0c3ff',
    'color-primary-dark-5': '#e7e1ff',
    'color-primary-dark-6': '#e7e1ff',
    'color-primary-dark-7': '#f3f0ff',
    'color-primary-light-1': '#8968ff',
    'color-primary-light-2': '#7a55f5',
    'color-primary-light-3': '#6b46e5',
    'color-primary-light-4': '#5b34d1',
    'color-primary-light-5': '#5b34d1',
    'color-primary-light-6': '#4c2ab0',
    'color-primary-light-7': '#4c2ab0',
    'color-primary-alpha-10': '#8968ff19',
    'color-primary-alpha-20': '#8968ff33',
    'color-primary-alpha-30': '#8968ff4b',
    'color-primary-alpha-40': '#8968ff66',
    'color-primary-alpha-50': '#8968ff80',
    'color-primary-alpha-60': '#8968ff99',
    'color-primary-alpha-70': '#8968ffb3',
    'color-primary-alpha-80': '#8968ffcc',
    'color-primary-alpha-90': '#8968ffe1',
  },
};

let failed = false;
for (const filename of process.argv.slice(2)) {
  const theme = path.basename(filename).includes('dark') ? 'dark' : 'light';
  const body = fs.readFileSync(filename, 'utf8');
  let fileFailed = false;
  for (const [variable, expected] of Object.entries(palettes[theme])) {
    const pattern = new RegExp(`--${variable}\\s*:\\s*(#[0-9a-fA-F]+)`, 'g');
    const values = Array.from(body.matchAll(pattern), (match) => match[1]);
    const normalized = values.map((value) => value.toLowerCase());
    if (normalized.length !== 1 || normalized[0] !== expected) {
      console.error(`${filename}: expected exactly one --${variable}: ${expected}; found ${values.length ? values.join(', ') : 'none'}`);
      failed = true;
      fileFailed = true;
    }
  }
  if (!fileFailed) console.log(`GitW3 ${theme} palette verified in ${filename}`);
}

if (failed) process.exit(1);
JS
