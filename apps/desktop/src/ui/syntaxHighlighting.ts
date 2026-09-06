import {
  bundledLanguages,
  bundledLanguagesInfo,
  getSingletonHighlighter,
  type BundledLanguage,
  type Highlighter,
  type ThemedTokenWithVariants,
} from "shiki";

export type SyntaxLanguage = BundledLanguage;
export type SyntaxToken = ThemedTokenWithVariants;
export type SyntaxTokens = readonly (readonly SyntaxToken[])[];

let highlighterPromise: Promise<Highlighter> | undefined;
const languageLookup = languageMetadata();

export function syntaxHighlightingLanguageHints(): string[] {
  return [...languageLookup.keys()];
}

export function resolveSyntaxLanguage(languageHint: string): SyntaxLanguage | undefined {
  return languageLookup.get(languageHint) ?? syntaxLanguageForSourcePath(languageHint);
}

export async function highlightSyntax(code: string, language: SyntaxLanguage): Promise<SyntaxTokens> {
  const highlighter = await getHighlighter();
  await highlighter.loadLanguage(language);
  return highlighter.codeToTokensWithThemes(code, {
    lang: language,
    themes: { dark: "github-dark", light: "github-light" },
  });
}

async function getHighlighter(): Promise<Highlighter> {
  highlighterPromise ??= getSingletonHighlighter({
    langs: [],
    themes: ["github-light", "github-dark"],
  }).catch((error: unknown) => {
    highlighterPromise = undefined;
    throw error;
  });
  return highlighterPromise;
}

function languageMetadata(): Map<string, BundledLanguage> {
  const result = new Map<string, BundledLanguage>();
  for (const info of bundledLanguagesInfo) {
    if (!isBundledLanguage(info.id)) continue;
    addLanguageForms(result, info.id, info.id);
    addLanguageForms(result, info.name, info.id);
    for (const alias of info.aliases ?? []) addLanguageForms(result, alias, info.id);
  }
  return result;
}

function addLanguageForms(
  lookup: Map<string, BundledLanguage>,
  display: string,
  canonical: BundledLanguage,
): void {
  lookup.set(display, canonical);
  lookup.set(display.toLowerCase(), canonical);
  lookup.set(display.toUpperCase(), canonical);
}

function isBundledLanguage(value: string): value is BundledLanguage {
  return value in bundledLanguages;
}

function syntaxLanguageForSourcePath(path: string): BundledLanguage | undefined {
  const extension = sourceExtension(sourceFilename(path));
  return extension === undefined ? undefined : languageLookup.get(extension);
}

function sourceFilename(path: string): string {
  let offset = 0;
  for (let index = 0; index < path.length; index += 1) {
    if (path[index] === "/" || path[index] === "\\") offset = index + 1;
  }
  return path.slice(offset);
}

function sourceExtension(filename: string): string | undefined {
  for (let index = filename.length - 1; index > 0; index -= 1) {
    if (filename[index] === ".") return filename.slice(index + 1).toLowerCase();
  }
  return undefined;
}
