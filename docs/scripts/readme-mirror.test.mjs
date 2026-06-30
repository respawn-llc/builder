import test from 'node:test';
import assert from 'node:assert/strict';

import { mirrorRepoMarkdownDocument, mirrorReadme } from './readme-mirror.mjs';

const docsConfig = {
  docsHomePath: '/docs/',
  contributingPath: '/contributing/',
  securityPath: '/security/',
  docsHomeTitle: 'Home',
  repoUrl: 'https://example.com/repo',
  repoDefaultBranch: 'main',
  repoEditRootUrl: 'https://example.com/repo/edit/main/',
  repoBlobRootUrl: 'https://example.com/repo/blob/main/',
  repoRawRootUrl: 'https://raw.example.com/repo/main/',
};

test('mirrorReadme removes the root heading and adds Starlight frontmatter', () => {
  const mirrored = mirrorReadme('# Kent\n\nBody text.\n', docsConfig);

  assert.match(mirrored, /^---\ntitle: Home\neditUrl: https:\/\/example\.com\/repo\/edit\/main\/README\.md\n---\n/);
  assert.match(mirrored, /Body text\./);
  assert.doesNotMatch(mirrored, /^# Kent/m);
});

test('mirrorRepoMarkdownDocument rewrites relative markdown and html urls to docs and repository targets', () => {
  const mirrored = mirrorRepoMarkdownDocument(
    [
      '# Source title',
      '',
      '[readme](README.md#usage)',
      '[contributing](CONTRIBUTING.md)',
      '[nested](docs/architecture.md)',
      '![image](assets/screenshot.png)',
      '<a href="SECURITY.md">security</a>',
      '<img src="images/logo.svg">',
      '[absolute](https://example.org/page)',
      '[fragment](#local)',
      '',
    ].join('\n'),
    docsConfig,
    {
      title: 'Mirrored',
      editPath: 'README.md',
    },
  );

  assert.match(mirrored, /\[readme\]\(\/docs\/#usage\)/);
  assert.match(mirrored, /\[contributing\]\(\/contributing\/\)/);
  assert.match(mirrored, /\[nested\]\(https:\/\/example\.com\/repo\/blob\/main\/docs\/architecture\.md\)/);
  assert.match(mirrored, /!\[image\]\(https:\/\/raw\.example\.com\/repo\/main\/assets\/screenshot\.png\)/);
  assert.match(mirrored, /<a href="\/security\/">security<\/a>/);
  assert.match(mirrored, /<img src="https:\/\/raw\.example\.com\/repo\/main\/images\/logo\.svg">/);
  assert.match(mirrored, /\[absolute\]\(https:\/\/example\.org\/page\)/);
  assert.match(mirrored, /\[fragment\]\(#local\)/);
});

test('mirrorRepoMarkdownDocument preserves complete raw html elements while rewriting urls', () => {
  const mirrored = mirrorRepoMarkdownDocument(
    '<DIV><A href="SECURITY.md">security</A><IMG src="assets/logo.png"></DIV>\n',
    docsConfig,
    {
      title: 'Mirrored',
      editPath: 'README.md',
    },
  );

  assert.match(
    mirrored,
    /<div><a href="\/security\/">security<\/a><img src="https:\/\/raw\.example\.com\/repo\/main\/assets\/logo\.png"><\/div>/,
  );
});

test('mirrorRepoMarkdownDocument preserves split raw html opening tags while rewriting urls', () => {
  const mirrored = mirrorRepoMarkdownDocument(
    'Before <a data-token="kent-raw-html-opening-tag-sentinel-0" href="SECURITY.md">security</a> after.\n',
    docsConfig,
    {
      title: 'Mirrored',
      editPath: 'README.md',
    },
  );

  assert.match(
    mirrored,
    /Before <a data-token="kent-raw-html-opening-tag-sentinel-0" href="\/security\/">security<\/a> after\./,
  );
});

test('mirrorRepoMarkdownDocument preserves child markup in split raw html opening fragments', () => {
  const mirrored = mirrorRepoMarkdownDocument(
    [
      '<details><summary><a href="SECURITY.md">security</a></summary>',
      '',
      '[readme](README.md)',
      '',
      '</details>',
      '',
    ].join('\n'),
    docsConfig,
    {
      title: 'Mirrored',
      editPath: 'README.md',
    },
  );

  assert.match(mirrored, /<details><summary><a href="\/security\/">security<\/a><\/summary>/);
  assert.match(mirrored, /\[readme\]\(\/docs\/\)/);
  assert.match(mirrored, /<\/details>/);
});
