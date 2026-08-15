import { unlink } from "node:fs/promises";
import path from "node:path";

async function removeIfExists(filePath) {
  try {
    await unlink(filePath);
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

export async function removeLegacyMirroredDocuments(
  legacyOutputDirectory,
  mirroredDocuments,
) {
  await Promise.all(
    mirroredDocuments.map((document) =>
      removeIfExists(path.join(legacyOutputDirectory, document.outputFileName)),
    ),
  );
}
