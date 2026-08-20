import {
  cp,
  mkdir,
  readdir,
  readFile,
  unlink,
  writeFile,
} from "node:fs/promises";
import path from "node:path";

const MARKDOWN_EXTENSIONS = new Set([".md", ".mdx"]);
const EXCLUDED_ENTRY_IDS = new Set(["404"]);
const MANIFEST_FILE_NAME = ".kent-docs-markdown-endpoints.json";

async function collectMarkdownFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const nestedFiles = await Promise.all(
    entries.map(async (entry) => {
      const entryPath = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        return collectMarkdownFiles(entryPath);
      }
      if (entry.isFile() && MARKDOWN_EXTENSIONS.has(path.extname(entry.name))) {
        return [entryPath];
      }
      return [];
    }),
  );
  return nestedFiles.flat().sort();
}

async function collectMarkdownFilesIfPresent(directory) {
  try {
    return await collectMarkdownFiles(directory);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return [];
    }
    throw error;
  }
}

async function removeFileIfPresent(filePath) {
  try {
    await unlink(filePath);
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

function toPosixPath(value) {
  return value.split(path.sep).join(path.posix.sep);
}

export function docsEntryId(sourceDirectory, filePath) {
  const relativePath = toPosixPath(path.relative(sourceDirectory, filePath));
  const parsedPath = path.posix.parse(relativePath);
  return parsedPath.dir.length === 0
    ? parsedPath.name
    : path.posix.join(parsedPath.dir, parsedPath.name);
}

export function markdownOutputPath(outputDirectory, entryId) {
  return path.join(outputDirectory, `${entryId}.md`);
}

function detectDuplicateEntryIds(sourceFiles) {
  const seenFiles = new Map();

  for (const sourceFile of sourceFiles) {
    const previousFilePath = seenFiles.get(sourceFile.entryId);
    if (previousFilePath) {
      throw new Error(
        `duplicate docs markdown endpoint "${sourceFile.entryId}" from ${previousFilePath} and ${sourceFile.filePath}`,
      );
    }
    seenFiles.set(sourceFile.entryId, sourceFile.filePath);
  }
}

async function readPreviousManifest(outputDirectory) {
  try {
    const manifestContent = await readFile(
      path.join(outputDirectory, MANIFEST_FILE_NAME),
      "utf8",
    );
    const parsedManifest = JSON.parse(manifestContent);
    return Array.isArray(parsedManifest?.entryIds)
      ? parsedManifest.entryIds.filter((entryId) => typeof entryId === "string")
      : [];
  } catch (error) {
    if (error?.code === "ENOENT") {
      return [];
    }
    throw error;
  }
}

async function removeStaleMarkdownEndpoints(outputDirectory, nextEntryIds) {
  const nextEntryIdSet = new Set(nextEntryIds);
  const previousEntryIds = await readPreviousManifest(outputDirectory);
  const staleEntryIds = previousEntryIds.filter(
    (entryId) => !nextEntryIdSet.has(entryId),
  );

  await Promise.all(
    staleEntryIds.map((entryId) =>
      removeFileIfPresent(markdownOutputPath(outputDirectory, entryId)),
    ),
  );
}

async function writeManifest(outputDirectory, entryIds) {
  await writeFile(
    path.join(outputDirectory, MANIFEST_FILE_NAME),
    `${JSON.stringify({ entryIds }, null, 2)}\n`,
    "utf8",
  );
}

export async function emitMarkdownEndpoints({
  sourceDirectories,
  outputDirectory,
}) {
  const sourceFiles = (
    await Promise.all(
      sourceDirectories.map(async (sourceDirectory) => {
        const files = await collectMarkdownFilesIfPresent(sourceDirectory);
        return files.map((filePath) => ({
          entryId: docsEntryId(sourceDirectory, filePath),
          filePath,
        }));
      }),
    )
  )
    .flat()
    .filter(({ entryId }) => !EXCLUDED_ENTRY_IDS.has(entryId));
  const entryIds = sourceFiles.map(({ entryId }) => entryId).sort();

  detectDuplicateEntryIds(sourceFiles);
  await removeStaleMarkdownEndpoints(outputDirectory, entryIds);
  await Promise.all(
    sourceFiles.map(async ({ entryId, filePath }) => {
      const outputPath = markdownOutputPath(outputDirectory, entryId);
      await mkdir(path.dirname(outputPath), { recursive: true });
      await cp(filePath, outputPath);
    }),
  );
  await writeManifest(outputDirectory, entryIds);

  return entryIds;
}

export async function appendMarkdownDiscovery({
  llmsPath,
  markdownEntryIds,
  docsConfig,
}) {
  const llmsContent = await readFile(llmsPath, "utf8");
  const markdownLinks = markdownEntryIds
    .map(
      (entryId) =>
        `- [${entryId}](${docsConfig.getPublicUrl(`/${entryId}.md`)})`,
    )
    .join("\n");

  await writeFile(
    llmsPath,
    `${llmsContent.trimEnd()}\n\n## Raw Markdown Pages\n\n${markdownLinks}\n`,
    "utf8",
  );
}
