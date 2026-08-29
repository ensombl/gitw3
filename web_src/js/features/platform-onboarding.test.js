import {createPrivateKey, createPublicKey} from 'node:crypto';
import {expect, test} from 'vitest';
import {bytesToBase58, generatePlatformKeyPair} from './platform-onboarding.ts';

function base58ToBytes(value) {
  const alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
  const bytes = [0];
  for (const character of value) {
    let carry = alphabet.indexOf(character);
    for (let index = 0; index < bytes.length; index++) {
      carry += bytes[index] * 58;
      bytes[index] = carry & 0xff;
      carry >>= 8;
    }
    while (carry > 0) {
      bytes.push(carry & 0xff);
      carry >>= 8;
    }
  }
  for (let index = 0; value[index] === '1' && index < value.length - 1; index++) bytes.push(0);
  return Uint8Array.from(bytes.reverse());
}

test('encodes base58btc values', () => {
  expect(bytesToBase58(Uint8Array.from([0]))).toEqual('1');
  expect(bytesToBase58(new TextEncoder().encode('Hello World'))).toEqual('JxF12TrwUP45BMd');
});

test('generates an importable P-256 key without Web Crypto', async () => {
  const generated = await generatePlatformKeyPair(null);
  const privateKey = createPrivateKey({
    key: Buffer.from(generated.privateKeyPkcs8),
    format: 'der',
    type: 'pkcs8',
  });
  const publicKey = createPublicKey(privateKey).export({format: 'der', type: 'spki'});

  expect(base58ToBytes(bytesToBase58(generated.publicKeySpki))).toEqual(generated.publicKeySpki);
  expect(new Uint8Array(publicKey)).toEqual(generated.publicKeySpki);
});
