import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import { resolveMirroredDocsPaths, syncMirroredDocs } from './sync-mirrored-docs.mjs';

const docsConfig = {
  docsHomePath: '/docs/',
  contributingPath: '/contributing/',
  securityPath: '/security/',
  repoUrl: 'https://example.com/repo',
  repoDefaultBranch: 'main',
  repoEditRootUrl: 'https://example.com/repo/edit/main/',
  repoBlobRootUrl: 'https://example.com/repo/blob/main/',
  repoRawRootUrl: 'https://raw.example.com/repo/main/',
};

async function createMirroredRepoFixture() {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'kent-mirrored-docs-'));
  const docsRoot = path.join(tempRoot, 'docs');
  const repoRoot = path.join(tempRoot, 'repo');

  await mkdir(path.join(docsRoot, 'src', 'content', 'docs'), { recursive: true });
  await mkdir(path.join(docsRoot, '.generated', 'content', 'docs'), { recursive: true });
  await mkdir(repoRoot, { recursive: true });
  await writeFile(path.join(repoRoot, 'README.md'), '# Kent\n\nReadme body.\n', 'utf8');
  await writeFile(path.join(repoRoot, 'CONTRIBUTING.md'), '# Contributing\n\nContribution body.\n', 'utf8');
  await writeFile(path.join(repoRoot, 'SECURITY.md'), '# Security\n\nSecurity body.\n', 'utf8');

  return { docsRoot, repoRoot };
}

test('syncMirroredDocs writes generated mirrored docs and removes legacy copies', async () => {
  const { docsRoot, repoRoot } = await createMirroredRepoFixture();
  const paths = resolveMirroredDocsPaths(docsRoot);

  await writeFile(path.join(paths.legacyOutputDirectory, 'docs.md'), 'legacy docs\n', 'utf8');
  await writeFile(path.join(paths.deprecatedGeneratedOutputDirectory, 'security.md'), 'legacy security\n', 'utf8');

  await syncMirroredDocs({ docsRoot, repoRoot, docsConfig });

  assert.deepEqual((await readdir(paths.outputDirectory)).sort(), ['contributing.md', 'docs.md', 'security.md']);
  await assert.rejects(readFile(path.join(paths.legacyOutputDirectory, 'docs.md'), 'utf8'));
  await assert.rejects(readFile(path.join(paths.deprecatedGeneratedOutputDirectory, 'security.md'), 'utf8'));
});
