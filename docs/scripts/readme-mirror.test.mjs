import test from 'node:test';
import assert from 'node:assert/strict';

import { mirroredDocuments } from './mirrored-documents.mjs';
import { mirrorRepoMarkdownDocument } from './readme-mirror.mjs';
import { resolveDocsConfig } from './site-config.mjs';

test('mirrored root documents keep repository-root edit URLs', () => {
  const docsConfig = resolveDocsConfig({});
  const expectedEditUrls = new Map([
    ['README.md', 'https://github.com/respawn-llc/kent/edit/main/README.md'],
    ['CONTRIBUTING.md', 'https://github.com/respawn-llc/kent/edit/main/CONTRIBUTING.md'],
    ['SECURITY.md', 'https://github.com/respawn-llc/kent/edit/main/SECURITY.md'],
  ]);

  for (const document of mirroredDocuments) {
    const mirroredMarkdown = mirrorRepoMarkdownDocument('# Title\n\nBody.\n', docsConfig, document);
    const [openingFence, titleLine, editUrlLine, closingFence] = mirroredMarkdown.split('\n', 4);

    assert.equal(openingFence, '---');
    assert.equal(titleLine, `title: ${document.title}`);
    assert.equal(editUrlLine, `editUrl: ${expectedEditUrls.get(document.sourcePath)}`);
    assert.equal(closingFence, '---');
  }
});
