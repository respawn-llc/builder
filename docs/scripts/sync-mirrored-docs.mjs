import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import path from "node:path";

import { removeLegacyMirroredDocuments } from "./legacy-mirrored-documents.mjs";
import { mirrorRepoMarkdownDocument } from "./readme-mirror.mjs";
import { mirroredDocuments } from "./mirrored-documents.mjs";
import { resolveDocsConfig } from "./site-config.mjs";

export function resolveMirroredDocsPaths(docsRoot) {
  return {
    outputDirectory: path.join(
      docsRoot,
      "src",
      ".generated",
      "content",
      "docs",
    ),
    legacyOutputDirectory: path.join(docsRoot, "src", "content", "docs"),
    deprecatedGeneratedOutputDirectory: path.join(
      docsRoot,
      ".generated",
      "content",
      "docs",
    ),
  };
}

const syncQueues = new Map();

export async function writeFileAtomically(filePath, contents) {
  const temporaryFilePath = `${filePath}.tmp-${randomUUID()}`;
  await writeFile(temporaryFilePath, contents, "utf8");
  await rename(temporaryFilePath, filePath);
}

async function performSyncMirroredDocs({ docsRoot, repoRoot, docsConfig }) {
  const {
    outputDirectory,
    legacyOutputDirectory,
    deprecatedGeneratedOutputDirectory,
  } = resolveMirroredDocsPaths(docsRoot);

  await mkdir(outputDirectory, { recursive: true });
  await removeLegacyMirroredDocuments(legacyOutputDirectory, mirroredDocuments);
  await removeLegacyMirroredDocuments(
    deprecatedGeneratedOutputDirectory,
    mirroredDocuments,
  );

  const results = await Promise.allSettled(
    mirroredDocuments.map(async (document) => {
      const sourceFilePath = path.join(repoRoot, document.sourcePath);
      const outputFilePath = path.join(
        outputDirectory,
        document.outputFileName,
      );
      const sourceMarkdown = await readFile(sourceFilePath, "utf8");
      const mirroredMarkdown = mirrorRepoMarkdownDocument(
        sourceMarkdown,
        docsConfig,
        {
          title: document.title,
          editPath: document.editPath,
        },
      );
      await writeFileAtomically(outputFilePath, mirroredMarkdown);
    }),
  );

  const failures = results.filter((result) => result.status === "rejected");
  if (failures.length === 0) {
    return;
  }
  if (failures.length === 1) {
    throw failures[0].reason;
  }
  throw new AggregateError(
    failures.map((result) => result.reason),
    "sync mirrored docs failed",
  );
}

export async function syncMirroredDocs({
  docsRoot,
  repoRoot,
  docsConfig = resolveDocsConfig(),
}) {
  const previousSync = syncQueues.get(docsRoot) ?? Promise.resolve();
  const nextSync = previousSync
    .catch(() => {})
    .then(() => performSyncMirroredDocs({ docsRoot, repoRoot, docsConfig }));

  syncQueues.set(docsRoot, nextSync);

  try {
    await nextSync;
  } finally {
    if (syncQueues.get(docsRoot) === nextSync) {
      syncQueues.delete(docsRoot);
    }
  }
}
