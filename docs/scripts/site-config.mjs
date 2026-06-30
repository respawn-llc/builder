const DEFAULT_SITE_URL = 'https://kent.sh';
const DEFAULT_BASE_PATH = '';
const SITE_TITLE = 'Kent';
const DOCS_HOME_LABEL = 'Kent';
const DOCS_HOME_TITLE = 'Home';
const DOCS_HOME_VISIBLE_TITLE = 'Kent';
const DOCS_HOME_SLUG = 'docs';
const DOCS_HOME_PATH = '/docs/';
const CONTRIBUTING_PATH = '/contributing/';
const SECURITY_PATH = '/security/';
const REPO_URL = 'https://github.com/respawn-llc/kent';
const REPO_DEFAULT_BRANCH = 'main';
const REPO_EDIT_ROOT_URL = `${REPO_URL}/edit/${REPO_DEFAULT_BRANCH}/`;
const REPO_BLOB_ROOT_URL = `${REPO_URL}/blob/${REPO_DEFAULT_BRANCH}/`;
const REPO_RAW_ROOT_URL = `https://raw.githubusercontent.com/respawn-llc/kent/${REPO_DEFAULT_BRANCH}/`;
const DOCSEARCH_APP_ID = 'YFIMJHUME7';
const DOCSEARCH_API_KEY = '87f58a573c52b1bb4aa289030dbb9ed9';
const DOCSEARCH_INDEX_NAME = 'kent';
const SOCIAL_PREVIEW_PATH = '/kent-social-preview.webp';

function firstNonEmpty(value) {
    if (typeof value !== 'string') {
        return undefined;
    }

    const trimmed = value.trim();
    return trimmed.length > 0 ? trimmed : undefined;
}

function stripTrailingSlash(value) {
    let next = value;
    while (next.length > 1 && next.endsWith('/')) {
        next = next.slice(0, -1);
    }
    return next;
}

function trimEdgeSlashes(value) {
    let start = 0;
    let end = value.length;

    while (start < end && value[start] === '/') {
        start += 1;
    }

    while (end > start && value[end - 1] === '/') {
        end -= 1;
    }

    return value.slice(start, end);
}

function normalizeSiteUrl(value) {
    return stripTrailingSlash(value);
}

function normalizeBasePath(value) {
    if (!value || value === '/') {
        return '';
    }

    const trimmed = trimEdgeSlashes(value);
    return trimmed.length === 0 ? '' : `/${trimmed}`;
}

function normalizeDomain(value) {
    if (!value) {
        return undefined;
    }

    const withoutProtocol = value.startsWith('http://')
        ? value.slice('http://'.length)
        : value.startsWith('https://')
            ? value.slice('https://'.length)
            : value;

    return stripTrailingSlash(withoutProtocol);
}

function joinPath(basePath, pathname) {
    const normalizedPathname = pathname.startsWith('/') ? pathname : `/${pathname}`;
    return `${basePath}${normalizedPathname}`;
}

export function resolveDocsConfig(env = process.env) {
    const siteUrl = normalizeSiteUrl(firstNonEmpty(env.DOCS_SITE_URL) ?? DEFAULT_SITE_URL);
    const basePath = normalizeBasePath(firstNonEmpty(env.DOCS_BASE_PATH) ?? DEFAULT_BASE_PATH);
    const customDomain = normalizeDomain(firstNonEmpty(env.DOCS_CUSTOM_DOMAIN));
    const docSearch = {
        appId: firstNonEmpty(env.DOCSEARCH_APP_ID) ?? DOCSEARCH_APP_ID,
        apiKey: firstNonEmpty(env.DOCSEARCH_API_KEY) ?? DOCSEARCH_API_KEY,
        indexName: firstNonEmpty(env.DOCSEARCH_INDEX_NAME) ?? DOCSEARCH_INDEX_NAME,
    };

    return {
        siteTitle: SITE_TITLE,
        docsHomeLabel: DOCS_HOME_LABEL,
        docsHomeTitle: DOCS_HOME_TITLE,
        docsHomeVisibleTitle: DOCS_HOME_VISIBLE_TITLE,
        docsHomeSlug: DOCS_HOME_SLUG,
        docsHomePath: DOCS_HOME_PATH,
        contributingPath: CONTRIBUTING_PATH,
        securityPath: SECURITY_PATH,
        siteUrl,
        basePath,
        customDomain,
        repoUrl: REPO_URL,
        repoDefaultBranch: REPO_DEFAULT_BRANCH,
        repoEditRootUrl: REPO_EDIT_ROOT_URL,
        repoBlobRootUrl: REPO_BLOB_ROOT_URL,
        repoRawRootUrl: REPO_RAW_ROOT_URL,
        socialPreviewPath: SOCIAL_PREVIEW_PATH,
        docSearch,
        getPublicUrl(pathname = '/') {
            const publicPath = joinPath(basePath, pathname);
            return new URL(publicPath, `${siteUrl}/`).toString();
        },
    };
}
