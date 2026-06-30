import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { fromHtml } from 'hast-util-from-html';
import { visit } from 'unist-util-visit';

import { resolveDocsConfig } from './site-config.mjs';

const currentFilePath = fileURLToPath(import.meta.url);
const docsRoot = path.dirname(path.dirname(currentFilePath));
const docsConfig = resolveDocsConfig();

async function findOpenPort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));

  if (!address || typeof address === 'string') {
    throw new Error('failed to allocate preview port');
  }

  return address.port;
}

async function waitForPreview(baseUrl, processOutput) {
  const deadline = Date.now() + 15_000;
  let lastError;

  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseUrl}${docsConfig.basePath}/llms.txt`);
      if (response.ok) {
        return;
      }
      lastError = new Error(`preview returned HTTP ${response.status}`);
    } catch (error) {
      lastError = error;
    }

    await new Promise((resolve) => setTimeout(resolve, 200));
  }

  throw new Error(`preview did not become ready: ${lastError?.message ?? 'unknown error'}\n${processOutput()}`);
}

async function fetchText(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`${url} returned HTTP ${response.status}`);
  }
  return response.text();
}

function anchorHrefs(html) {
  const hrefs = [];
  const tree = fromHtml(html, { fragment: true });

  visit(tree, 'element', (node) => {
    if (node.tagName !== 'a') {
      return;
    }

    const href = node.properties?.href;
    if (typeof href === 'string') {
      hrefs.push(href);
    }
  });

  return hrefs;
}

function assertAnchorHref(hrefs, expectedHref, pageUrl) {
  if (!hrefs.includes(expectedHref)) {
    throw new Error(`${pageUrl} is missing anchor href ${expectedHref}`);
  }
}

function assertNoAnchorHref(hrefs, unexpectedHref, pageUrl) {
  if (hrefs.includes(unexpectedHref)) {
    throw new Error(`${pageUrl} contains unexpected anchor href ${unexpectedHref}`);
  }
}

function assertNoGithubEditAnchor(hrefs, pageUrl) {
  const githubEditHref = hrefs.find((href) => href.startsWith(docsConfig.repoEditRootUrl));
  if (githubEditHref) {
    throw new Error(`${pageUrl} contains unexpected GitHub edit anchor href ${githubEditHref}`);
  }
}

const port = await findOpenPort();
const baseUrl = `http://127.0.0.1:${port}`;
const outputChunks = [];
const astroBin = path.join(docsRoot, 'node_modules', '.bin', 'astro');
const preview = spawn(astroBin, ['preview', '--host', '127.0.0.1', '--port', String(port)], {
  cwd: docsRoot,
  stdio: ['ignore', 'pipe', 'pipe'],
});
const processOutput = () => Buffer.concat(outputChunks).toString('utf8');

preview.stdout.on('data', (chunk) => outputChunks.push(chunk));
preview.stderr.on('data', (chunk) => outputChunks.push(chunk));

try {
  await waitForPreview(baseUrl, processOutput);

  const markdownUrl = `${baseUrl}${docsConfig.basePath}/command-postprocessing.md`;
  const sandboxingUrl = `${baseUrl}${docsConfig.basePath}/sandboxing/`;
  const sandboxingMarkdownUrl = `${baseUrl}${docsConfig.basePath}/sandboxing.md`;
  const serverUrl = `${baseUrl}${docsConfig.basePath}/server/`;
  const docsHomeUrl = `${baseUrl}${docsConfig.basePath}${docsConfig.docsHomePath}`;
  const notFoundUrl = `${baseUrl}${docsConfig.basePath}/404.html`;
  const [markdownText, , sandboxingMarkdown, sourceMarkdown, sandboxingSourceMarkdown] = await Promise.all([
    fetchText(markdownUrl),
    fetchText(sandboxingUrl),
    fetchText(sandboxingMarkdownUrl),
    readFile(path.join(docsRoot, 'src', 'content', 'docs', 'command-postprocessing.md'), 'utf8'),
    readFile(path.join(docsRoot, 'src', 'content', 'docs', 'sandboxing.md'), 'utf8'),
  ]);

  if (markdownText !== sourceMarkdown) {
    throw new Error(`${markdownUrl} does not match source markdown`);
  }
  if (sandboxingMarkdown !== sandboxingSourceMarkdown) {
    throw new Error(`${sandboxingMarkdownUrl} does not match source markdown`);
  }

  const [serverHtml, docsHomeHtml, notFoundHtml] = await Promise.all([
    fetchText(serverUrl),
    fetchText(docsHomeUrl),
    fetchText(notFoundUrl),
  ]);
  const serverHrefs = anchorHrefs(serverHtml);
  const docsHomeHrefs = anchorHrefs(docsHomeHtml);
  const notFoundHrefs = anchorHrefs(notFoundHtml);
  const expectedServerEditUrl = `${docsConfig.docsProjectEditRootUrl}src/content/docs/server.md`;
  const brokenServerEditUrl = `${docsConfig.repoEditRootUrl}src/content/docs/server.md`;

  assertAnchorHref(serverHrefs, expectedServerEditUrl, serverUrl);
  assertNoAnchorHref(serverHrefs, brokenServerEditUrl, serverUrl);
  assertAnchorHref(docsHomeHrefs, `${docsConfig.repoEditRootUrl}README.md`, docsHomeUrl);
  assertNoAnchorHref(docsHomeHrefs, `${docsConfig.docsProjectEditRootUrl}README.md`, docsHomeUrl);
  assertNoGithubEditAnchor(notFoundHrefs, notFoundUrl);
} finally {
  preview.kill('SIGTERM');
}
