import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

import { fromHtml } from 'hast-util-from-html';
import { visit } from 'unist-util-visit';

const currentFilePath = fileURLToPath(import.meta.url);
const docsRoot = path.dirname(path.dirname(currentFilePath));
const desktopPagePath = path.join(docsRoot, 'dist', 'desktop', 'index.html');
const releasePageUrl = 'https://github.com/respawn-llc/kent/releases/latest';

function nodeText(node) {
  if (node.type === 'text') {
    return node.value;
  }
  return Array.isArray(node.children) ? node.children.map(nodeText).join('') : '';
}

async function readBuiltRouteScript() {
  const tree = fromHtml(await readFile(desktopPagePath, 'utf8'));
  const routeScripts = [];
  visit(tree, 'element', (node) => {
    if (
      node.tagName === 'script' &&
      node.properties?.type === 'module' &&
      node.properties?.src === undefined &&
      nodeText(node).trim() !== ''
    ) {
      routeScripts.push(nodeText(node));
    }
  });
  assert.equal(routeScripts.length, 1, 'built desktop page must contain one inline module route script');
  return routeScripts[0];
}

const routeScript = await readBuiltRouteScript();

function modernWindowsNavigator(architecture = 'x86') {
  return {
    platform: '',
    userAgent: '',
    userAgentData: {
      platform: 'Windows',
      async getHighEntropyValues() {
        return { architecture };
      },
    },
  };
}

function legacyWindowsNavigator(platform) {
  return {
    platform,
    userAgent: '',
  };
}

async function runBuiltRoute({ navigator, assets = [], responseOk = true, responseStatus = 200 }) {
  let active = true;
  let context;
  let deadlineHandle;
  let statusWriteCount = 0;
  let resolveNavigation;
  const vmTimerHandles = new Map();
  const fetchController = new AbortController();
  const firstNavigation = new Promise((resolve) => {
    resolveNavigation = resolve;
  });

  const recordNavigation = (method, url) => {
    if (!active) {
      return;
    }
    active = false;
    resolveNavigation({ method, url });
  };
  const location = {
    assign(url) {
      recordNavigation('assign', url);
    },
    replace(url) {
      recordNavigation('replace', url);
    },
  };
  const statusElement = {};
  Object.defineProperty(statusElement, 'textContent', {
    set() {
      statusWriteCount += 1;
    },
  });

  const clearVmTimer = (handle) => {
    const clearTimer = vmTimerHandles.get(handle);
    if (clearTimer) {
      clearTimer(handle);
      vmTimerHandles.delete(handle);
    }
  };
  const setVmTimeout = (callback, delay, ...args) => {
    const handle = setTimeout(() => {
      vmTimerHandles.delete(handle);
      if (active) {
        callback(...args);
      }
    }, delay);
    vmTimerHandles.set(handle, clearTimeout);
    return handle;
  };
  const setVmInterval = (callback, delay, ...args) => {
    const handle = setInterval(() => {
      if (active) {
        callback(...args);
      }
    }, delay);
    vmTimerHandles.set(handle, clearInterval);
    return handle;
  };

  try {
    context = vm.createContext({
      AbortController,
      clearInterval: clearVmTimer,
      clearTimeout: clearVmTimer,
      console: {
        error() {},
        info() {},
        log() {},
        warn() {},
      },
      document: {
        getElementById(id) {
          return id === 'status' ? statusElement : null;
        },
      },
      async fetch() {
        if (fetchController.signal.aborted) {
          throw fetchController.signal.reason;
        }
        return {
          ok: responseOk,
          status: responseStatus,
          async json() {
            return { assets };
          },
        };
      },
      location,
      navigator,
      setInterval: setVmInterval,
      setTimeout: setVmTimeout,
      window: { location },
    });

    new vm.Script(routeScript, { filename: desktopPagePath }).runInContext(context, {
      timeout: 500,
    });
    const noNavigation = new Promise((_, reject) => {
      deadlineHandle = setTimeout(() => {
        reject(new Error('built desktop route did not navigate within 1 second'));
      }, 1_000);
    });
    const navigation = await Promise.race([firstNavigation, noNavigation]);
    return { navigation, statusWriteCount };
  } finally {
    if (deadlineHandle !== undefined) {
      clearTimeout(deadlineHandle);
    }
    for (const [handle, clearTimer] of vmTimerHandles) {
      clearTimer(handle);
    }
    vmTimerHandles.clear();
    fetchController.abort();
    active = false;
    resolveNavigation = () => {};
    context = undefined;
  }
}

function assertInstallerNavigation(result, installerUrl) {
  assert.deepEqual(result.navigation, { method: 'replace', url: installerUrl });
}

function assertFallbackNavigation(result) {
  assert.deepEqual(result.navigation, { method: 'assign', url: releasePageUrl });
  assert.ok(result.statusWriteCount >= 1, 'fallback must update the route status');
}

for (const [name, navigator] of [
  ['Windows', modernWindowsNavigator()],
  ['Win32', legacyWindowsNavigator('Win32')],
  ['Win64', legacyWindowsNavigator('Win64')],
  ['Windows on ARM', modernWindowsNavigator('arm64')],
]) {
  test(`${name} routes to the x64 NSIS installer`, { timeout: 2_000 }, async () => {
    const installerUrl = `https://example.test/${name}/Kent_1.2.3_x64-setup.exe`;
    const result = await runBuiltRoute({
      navigator,
      assets: [{ name: 'Kent_1.2.3_x64-setup.exe', browser_download_url: installerUrl }],
    });

    assertInstallerNavigation(result, installerUrl);
  });
}

test(
  'Windows ignores an updater signature ordered before the installer',
  { timeout: 2_000 },
  async () => {
    const installerUrl = 'https://example.test/Kent_1.2.3_x64-setup.exe';
    const result = await runBuiltRoute({
      navigator: modernWindowsNavigator(),
      assets: [
        {
          name: 'Kent_1.2.3_x64-setup.exe.sig',
          browser_download_url: 'https://example.test/Kent_1.2.3_x64-setup.exe.sig',
        },
        { name: 'Kent_1.2.3_x64-setup.exe', browser_download_url: installerUrl },
      ],
    });

    assertInstallerNavigation(result, installerUrl);
  },
);

test(
  'Windows falls back when the release has no installer',
  { timeout: 2_000 },
  async () => {
    const result = await runBuiltRoute({
      navigator: modernWindowsNavigator(),
      assets: [
        {
          name: 'Kent_1.2.3_x64-setup.exe.sig',
          browser_download_url: 'https://example.test/Kent_1.2.3_x64-setup.exe.sig',
        },
      ],
    });

    assertFallbackNavigation(result);
  },
);

test(
  'Windows falls back when the release API is not successful',
  { timeout: 2_000 },
  async () => {
    const result = await runBuiltRoute({
      navigator: modernWindowsNavigator(),
      responseOk: false,
      responseStatus: 503,
    });

    assertFallbackNavigation(result);
  },
);
