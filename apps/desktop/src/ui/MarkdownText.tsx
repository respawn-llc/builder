import type { Components } from "react-markdown";
import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";

import { safeExternalUrl } from "./externalLinks";
import "./MarkdownText.css";

export type MarkdownTextProps = Readonly<{
  value: string;
  onOpenLink?: (url: string) => void;
  inline?: boolean;
}>;

export function MarkdownText({ value, onOpenLink, inline = false }: MarkdownTextProps) {
  const rendered = (
    <ReactMarkdown
      components={markdownComponents(onOpenLink, inline)}
      rehypePlugins={[rehypeSanitize]}
      remarkPlugins={[remarkGfm]}
      skipHtml
    >
      {value}
    </ReactMarkdown>
  );
  if (inline) {
    return (
      <span className="markdown-text markdown-text-inline" data-testid="markdown-text-inline">
        {rendered}
      </span>
    );
  }
  return (
    <div className="markdown-text" data-testid="markdown-text">
      {rendered}
    </div>
  );
}

function markdownComponents(onOpenLink: MarkdownTextProps["onOpenLink"], inline: boolean): Components {
  return {
    ...(inline
      ? {
          p({ children }) {
            return <span>{children}</span>;
          },
        }
      : {}),
    a({ children, href }) {
      const safeHref = safeExternalUrl(href);
      if (safeHref === undefined) {
        return <span>{children}</span>;
      }
      return (
        <a
          href={safeHref}
          onClick={(event) => {
            if (onOpenLink === undefined) {
              return;
            }
            event.preventDefault();
            event.stopPropagation();
            onOpenLink(safeHref);
          }}
          rel="noreferrer"
          target="_blank"
        >
          {children}
        </a>
      );
    },
    code({ children }) {
      return <code>{children}</code>;
    },
    pre({ children }) {
      return <pre>{children}</pre>;
    },
  };
}
