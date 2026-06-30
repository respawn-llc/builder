import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import { resolveMirroredDocsPaths, syncMirroredDocs, writeFileAtomically } from './sync-mirrored-docs.mjs';

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

test('resolveMirroredDocsPaths keeps generated and legacy locations explicit', async () => {
  assert.deepEqual(resolveMirroredDocsPaths('/docs'), {
    outputDirectory: path.join('/docs', 'src', '.generated', 'content', 'docs'),
    legacyOutputDirectory: path.join('/docs', 'src', 'content', 'docs'),
    deprecatedGeneratedOutputDirectory: path.join('/docs', '.generated', 'content', 'docs'),
  });
});

test('syncMirroredDocs writes generated mirrored docs and removes legacy copies', async () => {
  const { docsRoot, repoRoot } = await createMirroredRepoFixture();
  const paths = resolveMirroredDocsPaths(docsRoot);

  await writeFile(path.join(paths.legacyOutputDirectory, 'docs.md'), 'legacy docs\n', 'utf8');
  await writeFile(path.join(paths.deprecatedGeneratedOutputDirectory, 'security.md'), 'legacy security\n', 'utf8');

  await syncMirroredDocs({ docsRoot, repoRoot, docsConfig });

  assert.match(await readFile(path.join(paths.outputDirectory, 'docs.md'), 'utf8'), /Readme body\./);
  assert.match(await readFile(path.join(paths.outputDirectory, 'contributing.md'), 'utf8'), /Contribution body\./);
  assert.match(await readFile(path.join(paths.outputDirectory, 'security.md'), 'utf8'), /Security body\./);
  await assert.rejects(readFile(path.join(paths.legacyOutputDirectory, 'docs.md'), 'utf8'));
  await assert.rejects(readFile(path.join(paths.deprecatedGeneratedOutputDirectory, 'security.md'), 'utf8'));
});

test('writeFileAtomically replaces file contents', async () => {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'kent-atomic-write-'));
  const filePath = path.join(tempRoot, 'target.md');

  await writeFile(filePath, 'before\n', 'utf8');
  await writeFileAtomically(filePath, 'after\n');

  assert.equal(await readFile(filePath, 'utf8'), 'after\n');
});
