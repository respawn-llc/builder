import path from "node:path";
import { fileURLToPath } from "node:url";

import { syncMirroredDocs } from "./sync-mirrored-docs.mjs";

const currentFilePath = fileURLToPath(import.meta.url);
const docsRoot = path.dirname(path.dirname(currentFilePath));
const repoRoot = path.dirname(docsRoot);

await syncMirroredDocs({ docsRoot, repoRoot });
