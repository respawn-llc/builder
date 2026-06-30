import test from 'node:test';
import assert from 'node:assert/strict';

import { resolveDocsConfig } from './site-config.mjs';

test('docs edit URLs keep repository-root and docs-project roots separate', () => {
  const docsConfig = resolveDocsConfig({});

  assert.equal(docsConfig.repoEditRootUrl, 'https://github.com/respawn-llc/kent/edit/main/');
  assert.equal(docsConfig.docsProjectEditRootUrl, 'https://github.com/respawn-llc/kent/edit/main/docs/');
  assert.notEqual(docsConfig.docsProjectEditRootUrl, docsConfig.repoEditRootUrl);
});
