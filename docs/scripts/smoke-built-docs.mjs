import { spawn } from 'node:child_process';
import { access, readFile, readdir } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { fromHtml } from 'hast-util-from-html';
import { visit } from 'unist-util-visit';

import { resolveDocsConfig } from './site-config.mjs';

const currentFilePath = fileURLToPath(import.meta.url);
const docsRoot = path.dirname(path.dirname(currentFilePath));
const distRoot = path.join(docsRoot, 'dist');
const docsConfig = resolveDocsConfig();

const representativePages = [
  {
    path: 'docs/index.html',
    canonicalPath: docsConfig.docsHomePath,
    requiresDescription: false,
  },
  {
    path: 'quickstart/index.html',
    canonicalPath: '/quickstart/',
    requiresDescription: true,
  },
  {
    path: 'sandboxing/index.html',
    canonicalPath: '/sandboxing/',
    requiresDescription: true,
  },
];

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

async function assertFileExists(relativePath) {
  const absolutePath = path.join(distRoot, relativePath);
  try {
    await access(absolutePath);
  } catch (error) {
    throw new Error(`expected built artifact ${relativePath} to exist`, { cause: error });
  }
}

function textContent(node) {
  if (node.type === 'text') {
    return node.value;
  }

  if (!Array.isArray(node.children)) {
    return '';
  }

  return node.children.map(textContent).join('');
}

function relIncludes(node, rel) {
  const value = node.properties?.rel;
  return Array.isArray(value) && value.includes(rel);
}

function propertyIncludes(node, property, expectedValue) {
  const value = node.properties?.[property];
  return Array.isArray(value) ? value.includes(expectedValue) : value === expectedValue;
}

function hasScriptWithSource(tree, sourcePart) {
  return collectElements(
    tree,
    (node) => node.tagName === 'script' && String(node.properties?.src ?? '').includes(sourcePart),
  ).length > 0;
}

function hasClass(node, className) {
  const value = node.properties?.className;
  return Array.isArray(value) && value.includes(className);
}

function collectElements(tree, predicate) {
  const elements = [];
  visit(tree, 'element', (node) => {
    if (predicate(node)) {
      elements.push(node);
    }
  });
  return elements;
}

function findMeta(tree, key, value) {
  return collectElements(
    tree,
    (node) => node.tagName === 'meta' && node.properties?.[key] === value,
  )[0];
}

async function assertRepresentativePage({ path: relativePath, canonicalPath, requiresDescription }) {
  const html = await readFile(path.join(distRoot, relativePath), 'utf8');
  const tree = fromHtml(html);

  const titles = collectElements(tree, (node) => node.tagName === 'title');
  assert(titles.length === 1, `${relativePath} should contain one <title>`);
  assert(textContent(titles[0]).trim().length > 0, `${relativePath} should contain title text`);

  const canonicalLinks = collectElements(
    tree,
    (node) => node.tagName === 'link' && relIncludes(node, 'canonical'),
  );
  assert(canonicalLinks.length === 1, `${relativePath} should contain one canonical link`);
  assert(
    canonicalLinks[0].properties?.href === docsConfig.getPublicUrl(canonicalPath),
    `${relativePath} canonical link should target the configured public URL`,
  );

  for (const [kind, key, value] of [
    ['robots', 'name', 'robots'],
    ['googlebot', 'name', 'googlebot'],
    ['Open Graph title', 'property', 'og:title'],
    ['Open Graph URL', 'property', 'og:url'],
    ['Open Graph image', 'property', 'og:image'],
    ['Twitter card', 'name', 'twitter:card'],
    ['Twitter image', 'name', 'twitter:image'],
  ]) {
    const meta = findMeta(tree, key, value);
    assert(meta?.properties?.content, `${relativePath} should contain ${kind} metadata`);
  }

  if (requiresDescription) {
    assert(
      findMeta(tree, 'name', 'description')?.properties?.content,
      `${relativePath} should contain description metadata`,
    );
    assert(
      findMeta(tree, 'property', 'og:description')?.properties?.content,
      `${relativePath} should contain Open Graph description metadata`,
    );
  }

  const stylesheetLinks = collectElements(
    tree,
    (node) => node.tagName === 'link' && relIncludes(node, 'stylesheet'),
  );
  assert(stylesheetLinks.length > 0, `${relativePath} should contain stylesheet links`);

  const mainElements = collectElements(tree, (node) => node.tagName === 'main');
  assert(mainElements.length === 1, `${relativePath} should contain one static <main>`);
  assert(
    textContent(mainElements[0]).trim().length > 200,
    `${relativePath} should contain crawlable main body text`,
  );

  const headings = collectElements(
    mainElements[0],
    (node) => node.tagName === 'h1' && node.properties?.id === '_top',
  );
  assert(headings.length === 1, `${relativePath} should contain one static h1#_top`);
  assert(textContent(headings[0]).trim().length > 0, `${relativePath} h1 should contain text`);

  const docSearchRoots = collectElements(tree, (node) => node.tagName === 'sl-doc-search');
  assert(docSearchRoots.length === 1, `${relativePath} should render one DocSearch root`);

  const docSearchButtons = collectElements(tree, (node) => hasClass(node, 'DocSearch-Button'));
  assert(docSearchButtons.length === 1, `${relativePath} should render one DocSearch trigger`);

  const localSearchRoots = collectElements(tree, (node) => node.tagName === 'site-search');
  assert(localSearchRoots.length === 0, `${relativePath} should not render local Pagefind search`);
}

async function assertRootRedirectPage() {
  const relativePath = 'index.html';
  const tree = fromHtml(await readFile(path.join(distRoot, relativePath), 'utf8'));
  const refreshMetas = collectElements(
    tree,
    (node) => node.tagName === 'meta' && propertyIncludes(node, 'httpEquiv', 'refresh'),
  );
  assert(refreshMetas.length === 1, `${relativePath} should contain one meta refresh`);
  assert(
    refreshMetas[0].properties?.content === `0; url=${docsConfig.docsHomePath}`,
    `${relativePath} should redirect to the docs home path`,
  );
  assert(
    collectElements(tree, (node) => node.tagName === 'a' && node.properties?.href === docsConfig.docsHomePath)
      .length === 1,
    `${relativePath} should contain a crawlable docs-home fallback link`,
  );
  assert(
    collectElements(tree, (node) => node.tagName === 'main').length === 0,
    `${relativePath} should remain a redirect shell`,
  );
  assert(!hasScriptWithSource(tree, 'ClientRouter'), `${relativePath} should not include ClientRouter`);
  assert(
    collectElements(tree, (node) => node.tagName === 'sl-doc-search').length === 0,
    `${relativePath} should not render DocSearch`,
  );
}

async function assertDesktopRedirectPage() {
  const relativePath = 'desktop/index.html';
  const tree = fromHtml(await readFile(path.join(distRoot, relativePath), 'utf8'));
  assert(
    findMeta(tree, 'name', 'robots')?.properties?.content === 'noindex',
    `${relativePath} should keep noindex robots metadata`,
  );
  assert(
    collectElements(tree, (node) => node.tagName === 'main').length === 1,
    `${relativePath} should contain static fallback content`,
  );
  assert(
    collectElements(
      tree,
      (node) =>
        node.tagName === 'a' && node.properties?.href === `${docsConfig.repoUrl}/releases/latest`,
    ).length === 1,
    `${relativePath} should link to the latest desktop release`,
  );
  assert(!hasScriptWithSource(tree, 'ClientRouter'), `${relativePath} should not include ClientRouter`);
  assert(
    collectElements(tree, (node) => node.tagName === 'sl-doc-search').length === 0,
    `${relativePath} should not render DocSearch`,
  );
}

async function assertNotFoundPage() {
  const relativePath = '404.html';
  const tree = fromHtml(await readFile(path.join(distRoot, relativePath), 'utf8'));
  assert(
    collectElements(tree, (node) => node.tagName === 'main').length === 1,
    `${relativePath} should contain static fallback content`,
  );
  assert(
    collectElements(tree, (node) => node.tagName === 'h1').length === 1,
    `${relativePath} should contain one heading`,
  );
  assert(
    findMeta(tree, 'name', 'robots')?.properties?.content ===
      'index,follow,max-image-preview:large,max-snippet:-1,max-video-preview:-1',
    `${relativePath} should keep the configured robots metadata`,
  );
}

async function walkFiles(root, prefix = '') {
  const entries = await readdir(path.join(root, prefix), { withFileTypes: true });
  const files = [];

  for (const entry of entries) {
    const relativePath = path.join(prefix, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walkFiles(root, relativePath)));
    } else {
      files.push(relativePath);
    }
  }

  return files;
}

async function assertNoPagefindArtifacts() {
  const files = await walkFiles(distRoot);
  const pagefindFiles = files.filter((filePath) => filePath.split(path.sep).includes('pagefind'));
  assert(pagefindFiles.length === 0, `expected no Pagefind artifacts, found ${pagefindFiles.join(', ')}`);
}

async function assertBuiltStaticArtifacts() {
  await Promise.all([
    ...representativePages.map(assertRepresentativePage),
    assertNotFoundPage(),
    assertRootRedirectPage(),
    assertDesktopRedirectPage(),
    assertFileExists('robots.txt'),
    assertFileExists('sitemap-index.xml'),
    assertFileExists('sitemap-0.xml'),
    assertFileExists('llms.txt'),
    assertFileExists('llms-full.txt'),
    assertFileExists('llms-small.txt'),
    assertFileExists('docs.md'),
    assertFileExists('quickstart.md'),
    assertFileExists('sandboxing.md'),
    assertNoPagefindArtifacts(),
  ]);
}

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
  await assertBuiltStaticArtifacts();
  await waitForPreview(baseUrl, processOutput);

  const markdownUrl = `${baseUrl}${docsConfig.basePath}/command-postprocessing.md`;
  const sandboxingUrl = `${baseUrl}${docsConfig.basePath}/sandboxing/`;
  const sandboxingMarkdownUrl = `${baseUrl}${docsConfig.basePath}/sandboxing.md`;
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
} finally {
  preview.kill('SIGTERM');
}
