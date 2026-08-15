import { unlink } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { glob } from "astro/loaders";

import { mirroredDocuments } from "./mirrored-documents.mjs";
import {
  resolveMirroredDocsPaths,
  syncMirroredDocs,
} from "./sync-mirrored-docs.mjs";

function buildDocsGlobPatterns() {
  const legacyMirroredFileNames = mirroredDocuments.map(
    (document) => path.posix.parse(document.outputFileName).name,
  );
  const excludedLegacyMirroredFiles = `!content/docs/{${legacyMirroredFileNames.join(",")}}.md`;

  return [
    "content/docs/**/*.md",
    excludedLegacyMirroredFiles,
    ".generated/content/docs/**/*.md",
  ];
}

function trimDocsPrefix(entryPath, prefix) {
  return entryPath.startsWith(prefix)
    ? entryPath.slice(prefix.length)
    : undefined;
}

function stripMarkdownExtension(entryPath) {
  const parsedPath = path.posix.parse(entryPath);
  return parsedPath.dir.length === 0
    ? parsedPath.name
    : path.posix.join(parsedPath.dir, parsedPath.name);
}

function generateDocsEntryId({ entry }) {
  const normalizedEntryPath = entry.split(path.sep).join(path.posix.sep);
  const relativeDocsPath =
    trimDocsPrefix(normalizedEntryPath, "content/docs/") ??
    trimDocsPrefix(normalizedEntryPath, ".generated/content/docs/");

  if (!relativeDocsPath) {
    throw new Error(`unsupported docs entry path: ${entry}`);
  }

  return stripMarkdownExtension(relativeDocsPath);
}

function createMirrorSourcePaths(repoRoot) {
  return mirroredDocuments.map((document) =>
    path.join(repoRoot, document.sourcePath),
  );
}

function findMirroredDocumentBySourcePath(changedPath, repoRoot) {
  const normalizedChangedPath = path.resolve(changedPath);
  return mirroredDocuments.find(
    (document) =>
      path.resolve(path.join(repoRoot, document.sourcePath)) ===
      normalizedChangedPath,
  );
}

async function removeIfExists(filePath) {
  try {
    await unlink(filePath);
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

export async function removeMirroredDocForSourcePath(
  changedPath,
  docsRoot,
  repoRoot,
) {
  const mirroredDocument = findMirroredDocumentBySourcePath(
    changedPath,
    repoRoot,
  );

  if (!mirroredDocument) {
    return false;
  }

  const { outputDirectory } = resolveMirroredDocsPaths(docsRoot);
  await removeIfExists(
    path.join(outputDirectory, mirroredDocument.outputFileName),
  );
  return true;
}

export function createDocsCollectionLoader() {
  const delegatedLoader = glob({
    base: "src",
    pattern: buildDocsGlobPatterns(),
    generateId: generateDocsEntryId,
  });
  const initializedWatchers = new WeakSet();

  return {
    name: "kent-docs-collection-loader",
    async load(context) {
      const docsRoot = fileURLToPath(context.config.root);
      const repoRoot = path.dirname(docsRoot);

      await syncMirroredDocs({ docsRoot, repoRoot });
      await delegatedLoader.load(context);

      if (!context.watcher || initializedWatchers.has(context.watcher)) {
        return;
      }

      initializedWatchers.add(context.watcher);
      const mirrorSourcePaths = createMirrorSourcePaths(repoRoot);
      context.watcher.add(mirrorSourcePaths);

      const reloadMirroredDocs = async (changedPath) => {
        if (!findMirroredDocumentBySourcePath(changedPath, repoRoot)) {
          return;
        }

        try {
          await syncMirroredDocs({ docsRoot, repoRoot });
        } catch (error) {
          context.logger.error(
            `Error syncing mirrored docs from ${changedPath}: ${error.message}`,
          );
        }
      };

      const removeMirroredDoc = async (changedPath) => {
        try {
          const removed = await removeMirroredDocForSourcePath(
            changedPath,
            docsRoot,
            repoRoot,
          );

          if (!removed) {
            return;
          }
        } catch (error) {
          context.logger.error(
            `Error removing mirrored docs for ${changedPath}: ${error.message}`,
          );
        }
      };

      context.watcher.on("add", reloadMirroredDocs);
      context.watcher.on("change", reloadMirroredDocs);
      context.watcher.on("unlink", removeMirroredDoc);
    },
  };
}
