import path from 'node:path';

import { fromHtml } from 'hast-util-from-html';
import { toHtml } from 'hast-util-to-html';
import { unified } from 'unified';
import remarkGfm from 'remark-gfm';
import remarkParse from 'remark-parse';
import remarkStringify from 'remark-stringify';
import { visit } from 'unist-util-visit';

const RAW_HTML_OPENING_TAG_SENTINEL_PREFIX = 'kent-raw-html-opening-tag-sentinel';
const VOID_HTML_ELEMENTS = new Set([
  'area',
  'base',
  'br',
  'col',
  'embed',
  'hr',
  'img',
  'input',
  'link',
  'meta',
  'source',
  'track',
  'wbr',
]);

function isFragmentOnly(url) {
  return url.startsWith('#') || url.startsWith('?');
}

function isAbsoluteUrl(url) {
  try {
    return Boolean(new URL(url).protocol);
  } catch {
    return false;
  }
}

function shouldRewriteUrl(url) {
  if (typeof url !== 'string' || url.length === 0) {
    return false;
  }

  if (isFragmentOnly(url) || url.startsWith('/')) {
    return false;
  }

  return !isAbsoluteUrl(url);
}

function splitHash(url) {
  const hashIndex = url.indexOf('#');
  if (hashIndex === -1) {
    return { pathname: url, hash: '' };
  }

  return {
    pathname: url.slice(0, hashIndex),
    hash: url.slice(hashIndex),
  };
}

function getMirroredRootDocumentPath(pathname, docsConfig) {
  const mirroredPaths = new Map([
    ['README.md', docsConfig.docsHomePath],
    ['CONTRIBUTING.md', docsConfig.contributingPath],
    ['SECURITY.md', docsConfig.securityPath],
  ]);

  return mirroredPaths.get(pathname);
}

function rewriteRelativeUrl(url, docsConfig) {
  const { pathname, hash } = splitHash(url);
  const normalizedPath = path.posix.normalize(pathname);
  const mirroredRootDocumentPath = getMirroredRootDocumentPath(normalizedPath, docsConfig);

  if (mirroredRootDocumentPath) {
    return `${mirroredRootDocumentPath}${hash}`;
  }

  const isDirectory = pathname.endsWith('/');
  const extension = path.posix.extname(normalizedPath).toLowerCase();
  const isImage = ['.avif', '.gif', '.jpeg', '.jpg', '.png', '.svg', '.webp'].includes(extension);

  if (isImage) {
    return new URL(normalizedPath, docsConfig.repoRawRootUrl).toString() + hash;
  }

  const targetRoot = isDirectory
    ? `${docsConfig.repoUrl}/tree/${docsConfig.repoDefaultBranch}/`
    : docsConfig.repoBlobRootUrl;
  return new URL(normalizedPath, `${targetRoot}`).toString() + hash;
}

function rewriteHtmlUrlProperties(html, docsConfig, { preserveOpeningTag = false } = {}) {
  if (html.trim().startsWith('</')) {
    return html;
  }

  const tree = fromHtml(html, { fragment: true });

  visit(tree, 'element', (node) => {
    const properties = node.properties ?? {};
    for (const propertyName of ['href', 'src', 'poster']) {
      const propertyValue = properties[propertyName];
      if (typeof propertyValue === 'string' && shouldRewriteUrl(propertyValue)) {
        properties[propertyName] = rewriteRelativeUrl(propertyValue, docsConfig);
      }
    }
  });

  const firstChild = tree.children[0];
  if (preserveOpeningTag && isSingleNonVoidHtmlElement(tree.children, firstChild)) {
    return serializeOpeningTag(firstChild);
  }

  return toHtml(tree);
}

function isSingleNonVoidHtmlElement(children, node) {
  return (
    children.length === 1 &&
    node?.type === 'element' &&
    !VOID_HTML_ELEMENTS.has(node.tagName)
  );
}

function serializeOpeningTag(node) {
  const serializedNode = toHtml(node);
  for (let index = 0; index < 100; index += 1) {
    const sentinel = `${RAW_HTML_OPENING_TAG_SENTINEL_PREFIX}-${index}`;
    if (serializedNode.includes(sentinel)) {
      continue;
    }

    const html = toHtml({
      ...node,
      children: [{ type: 'text', value: sentinel }],
    });
    const sentinelIndex = html.indexOf(sentinel);
    if (sentinelIndex !== -1) {
      return html.slice(0, sentinelIndex);
    }
  }

  throw new Error(`failed to preserve raw HTML opening tag <${node.tagName}> while mirroring docs`);
}

function getHtmlElementFromFragment(html) {
  const tree = fromHtml(html, { fragment: true });
  const firstChild = tree.children[0];
  return isSingleNonVoidHtmlElement(tree.children, firstChild) ? firstChild : undefined;
}

function closingHtmlTagName(html) {
  const trimmedHtml = html.trim();
  if (!trimmedHtml.startsWith('</') || !trimmedHtml.endsWith('>')) {
    return undefined;
  }

  const tagNameStart = 2;
  let tagNameEnd = tagNameStart;
  while (tagNameEnd < trimmedHtml.length && ![' ', '\t', '\n', '\r', '>'].includes(trimmedHtml[tagNameEnd])) {
    tagNameEnd += 1;
  }
  const tagName = trimmedHtml.slice(tagNameStart, tagNameEnd).toLowerCase();
  return tagName.length > 0 ? tagName : undefined;
}

function isSplitRawHtmlOpeningTag(node, index, parent) {
  if (node.type !== 'html' || typeof index !== 'number' || !parent?.children) {
    return false;
  }

  const element = getHtmlElementFromFragment(node.value);
  if (!element) {
    return false;
  }

  return parent.children
    .slice(index + 1)
    .some((sibling) => sibling.type === 'html' && closingHtmlTagName(sibling.value) === element.tagName);
}

function rewriteMarkdownHtmlNode(node, docsConfig, index, parent) {
  node.value = rewriteHtmlUrlProperties(node.value, docsConfig, {
    preserveOpeningTag: isSplitRawHtmlOpeningTag(node, index, parent),
  });
}

function buildFrontmatter(title, editUrl) {
  return [
    '---',
    `title: ${title}`,
    `editUrl: ${editUrl}`,
    '---',
    '',
  ].join('\n');
}

export function mirrorRepoMarkdownDocument(markdown, docsConfig, options) {
  const { title, editPath } = options;
  const processor = unified()
    .use(remarkParse)
    .use(remarkGfm)
    .use(() => (tree) => {
      const firstTopLevelHeadingIndex = tree.children.findIndex(
        (node) => node.type === 'heading' && node.depth === 1,
      );

      if (firstTopLevelHeadingIndex >= 0) {
        tree.children.splice(firstTopLevelHeadingIndex, 1);
      }

      visit(tree, (node, index, parent) => {
        if ((node.type === 'link' || node.type === 'image') && shouldRewriteUrl(node.url)) {
          node.url = rewriteRelativeUrl(node.url, docsConfig);
        }
        if (node.type === 'html') {
          rewriteMarkdownHtmlNode(node, docsConfig, index, parent);
        }
      });
    })
    .use(remarkGfm)
    .use(remarkStringify, {
      bullet: '-',
      fences: true,
      listItemIndent: 'one',
      rule: '-',
      strong: '*',
    });

  const transformedBody = String(processor.processSync(markdown)).trim();
  const frontmatter = buildFrontmatter(title, `${docsConfig.repoEditRootUrl}${editPath}`);

  return `${frontmatter}${transformedBody}\n`;
}

export function mirrorReadme(markdown, docsConfig) {
  return mirrorRepoMarkdownDocument(markdown, docsConfig, {
    title: docsConfig.docsHomeTitle,
    editPath: 'README.md',
  });
}
