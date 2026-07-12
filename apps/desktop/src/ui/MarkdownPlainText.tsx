import type { ReactNode } from "react";
import type { Components } from "react-markdown";
import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";

type TextChildren = Readonly<{ children?: ReactNode }>;

function text({ children }: TextChildren) {
  return <>{children}</>;
}

function textWithSpace({ children }: TextChildren) {
  return <>{children} </>;
}

const inlineElements = {
  a: text,
  blockquote: textWithSpace,
  br: () => <> </>,
  code: text,
  del: text,
  em: text,
  h1: textWithSpace,
  h2: textWithSpace,
  h3: textWithSpace,
  h4: textWithSpace,
  h5: textWithSpace,
  h6: textWithSpace,
  img: ({ alt }: Readonly<{ alt?: string | undefined }>) => <>{alt}</>,
  li: textWithSpace,
  ol: text,
  p: textWithSpace,
  pre: textWithSpace,
  strong: text,
  table: textWithSpace,
  tbody: text,
  td: textWithSpace,
  th: textWithSpace,
  thead: text,
  tr: textWithSpace,
  ul: text,
} satisfies Components;

export function MarkdownPlainText({ value }: Readonly<{ value: string }>) {
  return (
    <span className="markdown-plain-text">
      <ReactMarkdown components={inlineElements} rehypePlugins={[rehypeSanitize]} remarkPlugins={[remarkGfm]} skipHtml>
        {value}
      </ReactMarkdown>
    </span>
  );
}
