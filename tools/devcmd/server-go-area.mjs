export const serverGoDirectories = Object.freeze([
  "cli/app",
  "cli/kent",
  "cmd",
  "internal",
  "prompts",
  "server",
  "shared",
  "tools",
]);

export const serverPackagePatterns = Object.freeze(
  serverGoDirectories.map((directory) => `./${directory}/...`),
);
