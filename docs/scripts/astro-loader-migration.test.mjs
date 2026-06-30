import test from 'node:test';
import assert from 'node:assert/strict';

import { createDocsCollectionLoader } from './docs-collection-loader.mjs';

test('docs collection loader exposes the Astro content loader contract', () => {
  const loader = createDocsCollectionLoader();

  assert.equal(loader.name, 'kent-docs-collection-loader');
  assert.equal(typeof loader.load, 'function');
});
