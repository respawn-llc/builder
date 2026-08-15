import { access, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { resolveDocsConfig } from "./site-config.mjs";
import {
  appendMarkdownDiscovery,
  emitMarkdownEndpoints,
} from "./ai-readable-docs.mjs";

const currentFilePath = fileURLToPath(import.meta.url);
const docsRoot = path.dirname(path.dirname(currentFilePath));
const distRoot = path.join(docsRoot, "dist");
const docsConfig = resolveDocsConfig();

async function fileExists(filePath) {
  try {
    await access(filePath);
    return true;
  } catch {
    return false;
  }
}

async function resolveSitemapFilename() {
  const preferredFiles = ["sitemap-index.xml", "sitemap.xml"];

  for (const fileName of preferredFiles) {
    if (await fileExists(path.join(distRoot, fileName))) {
      return fileName;
    }
  }

  const distEntries = await readdir(distRoot);
  return distEntries.find(
    (entry) => entry.startsWith("sitemap") && entry.endsWith(".xml"),
  );
}

const sitemapFilename = await resolveSitemapFilename();
const robotsLines = ["User-agent: *", "Allow: /"];

if (sitemapFilename) {
  robotsLines.push(
    `Sitemap: ${docsConfig.getPublicUrl(`/${sitemapFilename}`)}`,
  );
}

robotsLines.push("");

await writeFile(
  path.join(distRoot, "robots.txt"),
  robotsLines.join("\n"),
  "utf8",
);

if (docsConfig.customDomain) {
  await writeFile(
    path.join(distRoot, "CNAME"),
    `${docsConfig.customDomain}\n`,
    "utf8",
  );
}

const markdownEntryIds = await emitMarkdownEndpoints({
  sourceDirectories: [
    path.join(docsRoot, "src", "content", "docs"),
    path.join(docsRoot, "src", ".generated", "content", "docs"),
  ],
  outputDirectory: distRoot,
});

await appendMarkdownDiscovery({
  llmsPath: path.join(distRoot, "llms.txt"),
  markdownEntryIds,
  docsConfig,
});
