import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

import { createDocsCollectionLoader, removeMirroredDocForSourcePath } from './docs-collection-loader.mjs';

class TestStore {
  #entries = new Map();

  keys() {
    return this.#entries.keys();
  }

  get(id) {
    return this.#entries.get(id);
  }

  set(entry) {
    this.#entries.set(entry.id, entry);
  }

  delete(id) {
    this.#entries.delete(id);
  }

  addModuleImport() {}

  addAssetImports() {}

  snapshot() {
    return new Map(this.#entries);
  }
}

class TestWatcher extends EventEmitter {
  addedPaths = [];

  add(paths) {
    this.addedPaths.push(...(Array.isArray(paths) ? paths : [paths]));
  }
}

async function createLoaderFixture() {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'kent-docs-loader-'));
  const docsRoot = path.join(tempRoot, 'docs');
  const repoRoot = tempRoot;

  await mkdir(path.join(docsRoot, 'src', 'content', 'docs'), { recursive: true });
  await mkdir(path.join(docsRoot, 'src', '.generated', 'content', 'docs'), { recursive: true });
  await writeFile(path.join(docsRoot, 'src', 'content', 'docs', 'quickstart.md'), '---\ntitle: Quickstart\n---\n\nBody\n', 'utf8');
  await writeFile(path.join(docsRoot, 'src', 'content', 'docs', 'docs.md'), 'legacy mirror must be ignored\n', 'utf8');
  await writeFile(path.join(repoRoot, 'README.md'), '# Kent\n\nReadme body.\n', 'utf8');
  await writeFile(path.join(repoRoot, 'CONTRIBUTING.md'), '# Contributing\n\nContribution body.\n', 'utf8');
  await writeFile(path.join(repoRoot, 'SECURITY.md'), '# Security\n\nSecurity body.\n', 'utf8');

  return { docsRoot, repoRoot };
}

function createLoaderContext(docsRoot, watcher) {
  return {
    collection: 'docs',
    config: {
      root: pathToFileURL(`${docsRoot}${path.sep}`),
      srcDir: pathToFileURL(`${path.join(docsRoot, 'src')}${path.sep}`),
    },
    logger: {
      warn() {},
      info() {},
      error(message) {
        throw new Error(message);
      },
    },
    watcher,
    store: new TestStore(),
    generateDigest(contents) {
      return createHash('sha256').update(contents).digest('hex');
    },
    async parseData({ data }) {
      return data;
    },
    entryTypes: new Map([
      [
        '.md',
        {
          async getEntryInfo({ contents }) {
            const titleMatch = contents.match(/^---\ntitle: (.+)\n---\n/s);
            return {
              body: contents,
              data: titleMatch ? { title: titleMatch[1] } : {},
            };
          },
        },
      ],
    ]),
  };
}

test('createDocsCollectionLoader syncs mirrored docs before delegating to Astro glob entries', async () => {
  const { docsRoot, repoRoot } = await createLoaderFixture();
  const watcher = new TestWatcher();
  const context = createLoaderContext(docsRoot, watcher);
  const loader = createDocsCollectionLoader();

  await loader.load(context);

  const entries = context.store.snapshot();
  assert.deepEqual([...entries.keys()].sort(), ['contributing', 'docs', 'quickstart', 'security']);
  assert.equal(entries.get('quickstart').filePath, 'src/content/docs/quickstart.md');
  assert.equal(entries.get('docs').filePath, 'src/.generated/content/docs/docs.md');
  assert.equal(await readFile(path.join(docsRoot, 'src', '.generated', 'content', 'docs', 'docs.md'), 'utf8'), entries.get('docs').body);
  assert.ok(watcher.addedPaths.includes(path.join(repoRoot, 'README.md')));
  assert.ok(watcher.addedPaths.includes(path.join(repoRoot, 'CONTRIBUTING.md')));
  assert.ok(watcher.addedPaths.includes(path.join(repoRoot, 'SECURITY.md')));
});

test('removeMirroredDocForSourcePath removes generated docs only for known mirrored sources', async () => {
  const { docsRoot, repoRoot } = await createLoaderFixture();
  const loader = createDocsCollectionLoader();
  const context = createLoaderContext(docsRoot);

  await loader.load(context);

  assert.equal(await removeMirroredDocForSourcePath(path.join(repoRoot, 'README.md'), docsRoot, repoRoot), true);
  await assert.rejects(readFile(path.join(docsRoot, 'src', '.generated', 'content', 'docs', 'docs.md'), 'utf8'));
  assert.equal(await removeMirroredDocForSourcePath(path.join(repoRoot, 'unmirrored.md'), docsRoot, repoRoot), false);
});
