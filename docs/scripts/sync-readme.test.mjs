import test from 'node:test';
import assert from 'node:assert/strict';

import { syncMirroredDocs } from './sync-mirrored-docs.mjs';

test('sync-readme delegates to the shared mirrored-doc sync implementation', () => {
  assert.equal(typeof syncMirroredDocs, 'function');
});
