import { useEffect, useState, type CSSProperties } from "react";

import {
  highlightSyntax,
  resolveSyntaxLanguage,
  type SyntaxLanguage,
  type SyntaxToken,
  type SyntaxTokens,
} from "./syntaxHighlighting";
import "./SyntaxHighlightedCode.css";

export type SyntaxHighlightedCodeProps = Readonly<{
  code: string;
  languageHint?: string | undefined;
  lineClassName?(lineIndex: number): string | undefined;
}>;

export function SyntaxHighlightedCode({ code, languageHint, lineClassName }: SyntaxHighlightedCodeProps) {
  const canonicalLanguage = languageHint === undefined ? undefined : resolveSyntaxLanguage(languageHint);
  const [highlighted, setHighlighted] = useState<{
    language: SyntaxLanguage;
    source: string;
    tokens: SyntaxTokens;
  } | null>(null);
  const [failure, setFailure] = useState<{
    language: SyntaxLanguage;
    source: string;
  } | null>(null);

  useEffect(() => {
    let active = true;
    if (canonicalLanguage === undefined) return () => void (active = false);
    void highlightSyntax(code, canonicalLanguage)
      .then((tokens) => {
        if (active) {
          setFailure(null);
          setHighlighted({ language: canonicalLanguage, source: code, tokens });
        }
      })
      .catch((error: unknown) => {
        if (active) {
          setFailure({ language: canonicalLanguage, source: code });
          reportError(error);
        }
      });
    return () => void (active = false);
  }, [canonicalLanguage, code]);

  const currentFailure = failure;
  if (
    currentFailure !== null &&
    currentFailure.language === canonicalLanguage &&
    currentFailure.source === code
  ) {
    return <PlainCode code={code} lineClassName={lineClassName} />;
  }
  const current = highlighted;
  const tokens =
    current !== null && current.language === canonicalLanguage && current.source === code
      ? current.tokens
      : null;
  if (tokens === null) return <PlainCode code={code} lineClassName={lineClassName} />;

  return (
    <pre className="syntax-highlighted-code">
      <code>{renderCodeLines(code, tokens, lineClassName)}</code>
    </pre>
  );
}

function PlainCode({
  code,
  lineClassName,
}: Readonly<{
  code: string;
  lineClassName?: ((lineIndex: number) => string | undefined) | undefined;
}>) {
  return (
    <pre className="syntax-highlighted-code">
      <code>{renderCodeLines(code, null, lineClassName)}</code>
    </pre>
  );
}

function renderCodeLines(
  code: string,
  tokens: SyntaxTokens | null,
  lineClassName: ((lineIndex: number) => string | undefined) | undefined,
) {
  if (lineClassName === undefined) {
    if (tokens === null) return code;
    return tokens.map((line, lineIndex) => (
      <span key={lineIndex}>
        <SyntaxTokenSpans tokens={line} />
        {lineIndex < tokens.length - 1 ? "\n" : null}
      </span>
    ));
  }

  return code.split("\n").map((sourceLine, lineIndex) => (
    <span className={syntaxLineClassName(lineClassName(lineIndex))} key={lineIndex}>
      {tokens === null ? sourceLine : <SyntaxTokenSpans tokens={tokens[lineIndex] ?? []} />}
    </span>
  ));
}

function syntaxLineClassName(customClassName: string | undefined): string {
  return customClassName === undefined
    ? "syntax-highlighted-code-line"
    : `syntax-highlighted-code-line ${customClassName}`;
}

function SyntaxTokenSpans({ tokens }: Readonly<{ tokens: readonly SyntaxToken[] }>) {
  return tokens.map((token, tokenIndex) => (
    <span className="syntax-highlighted-code-token" key={tokenIndex} style={tokenStyle(token)}>
      {token.content}
    </span>
  ));
}

interface TokenStyle extends CSSProperties {
  "--shiki-light"?: string;
  "--shiki-dark"?: string;
}

function tokenStyle(token: SyntaxToken): TokenStyle {
  const style: TokenStyle = {};
  if (token.variants.light?.color !== undefined) style["--shiki-light"] = token.variants.light.color;
  if (token.variants.dark?.color !== undefined) style["--shiki-dark"] = token.variants.dark.color;
  return style;
}
