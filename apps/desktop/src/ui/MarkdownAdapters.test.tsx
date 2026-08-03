import { StrictMode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StaticMarkdown, StreamingMarkdown, TaskBodyMarkdown } from "@/ui";

function taskBodyText(expected: string): HTMLElement {
  return screen.getByText(expected);
}

function markdownText(value: string): HTMLElement {
  return screen.getByText(
    (_, element) =>
      element !== null && element.classList.contains("markdown-text") && element.textContent.includes(value),
  );
}

describe("Markdown adapters", () => {
  it("exposes static, streaming, and task-body adapters", () => {
    render(
      <>
        <StaticMarkdown value="static value" />
        <StreamingMarkdown value="streaming value" />
        <TaskBodyMarkdown value="task-body value" />
      </>,
    );

    expect(screen.getByText("static value")).toBeInTheDocument();
    expect(markdownText("streaming value")).toBeInTheDocument();
    expect(screen.getByText("task-body value")).toBeInTheDocument();
  });

  it("replaces the current value without retaining stale output", () => {
    const view = render(<StaticMarkdown value="first value" />);

    expect(screen.getByText("first value")).toBeInTheDocument();
    view.rerender(<StaticMarkdown value="second value" />);

    expect(screen.getByText("second value")).toBeInTheDocument();
    expect(screen.queryByText("first value")).not.toBeInTheDocument();
  });

  it("mounts streaming content from its accumulated current value", async () => {
    render(<StreamingMarkdown value="accumulated value" />);

    await waitFor(() => {
      expect(markdownText("accumulated value")).toBeInTheDocument();
    });
  });

  it("renders GFM tables and strikethrough through Streamdown defaults", async () => {
    render(<StaticMarkdown value={"| Name |\n| --- |\n| ~~done~~ |"} />);

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByText("done")).toBeInTheDocument();
  });

  it("uses Streamdown's default sanitized HTML and link behavior", async () => {
    render(
      <StaticMarkdown
        value={'before <span>inner</span> [safe](https://example.com) [unsafe](javascript:alert(1))'}
      />,
    );

    expect(await screen.findByText("inner")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "safe" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "unsafe" })).not.toBeInTheDocument();
  });

  it("renders a 50,000-character value exactly", async () => {
    const value = "x".repeat(50_000);
    render(<StaticMarkdown value={value} />);

    expect(await screen.findByText(value)).toBeInTheDocument();
  });

  it("keeps Streamdown-reported incomplete code plain and selectable", async () => {
    render(<StreamingMarkdown value={"```javascript\nconst answer = 1;"} />);

    const code = await screen.findByText("const answer = 1;");
    expect(screen.queryByText("const", { exact: true })).not.toBeInTheDocument();
    expect(code).toBeInTheDocument();
  });

  it.each(["javascript", "js"])("highlights completed %s code", async (language) => {
    render(<StaticMarkdown value={`\`\`\`${language}\nconst answer = 1;\n\`\`\``} />);

    await waitFor(() => {
      expect(screen.getAllByText("const", { exact: true }).length).toBeGreaterThan(0);
    });
  });

  it("keeps collision-shaped completed blocks exact", async () => {
    render(
      <StaticMarkdown
        value={"```javascript\nconst first = 1;\n```\n\n```javascript\nconst second = 2;\n```"}
      />,
    );

    await waitFor(() => {
      expect(screen.getAllByText("const", { exact: true })).toHaveLength(2);
    });
    expect(screen.getByText("first", { exact: true })).toBeInTheDocument();
    expect(screen.getByText("second", { exact: true })).toBeInTheDocument();
  });

  it("keeps canonical and alias highlighting stable under StrictMode", async () => {
    render(
      <StrictMode>
        <StreamingMarkdown
          value={"```javascript\nconst canonical = 1;\n```\n\n```js\nconst alias = 2;\n```"}
        />
      </StrictMode>,
    );

    await waitFor(() => {
      expect(screen.getAllByText("const", { exact: true }).length).toBeGreaterThan(1);
    });
  });

  it("shows the exact current live text after appending prose", async () => {
    const view = render(
      <StreamingMarkdown value={"```javascript\nconst answer = 1;\n```\n\ntail-A"} />,
    );

    await waitFor(() => {
      expect(markdownText("tail-A")).toBeInTheDocument();
      expect(screen.getAllByText("const", { exact: true }).length).toBeGreaterThan(0);
    });

    view.rerender(
      <StreamingMarkdown value={"```javascript\nconst answer = 1;\n```\n\ntail-A tail-B"} />,
    );

    await waitFor(() => {
      expect(markdownText("tail-A tail-B")).toBeInTheDocument();
    });
  });
});

describe("TaskBodyMarkdown", () => {
  it.each([
    ["punctuation and formatting", "5 * 3 | 2\n\n**bold** *italic* ~~gone~~", "5 * 3 | 2 bold italic gone"],
    ["autolinks and reference labels", "<https://example.com> [label][ref]\n\n[ref]: https://example.com", "https://example.com label"],
    ["image alt text", "![image alt](image.png)", "image alt"],
    ["code payloads", "inline `x`\n\n```javascript\nconst x = 1;\n```", "inline x const x = 1;"],
  ])("projects %s to readable plain text", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected)).toHaveTextContent(expected);
  });

  it("keeps the server-bounded value intact and renders one plain span", () => {
    const value = "x".repeat(512);
    render(<TaskBodyMarkdown value={value} />);

    const element = taskBodyText(value);
    expect(element).toHaveClass("markdown-plain-text");
    expect(element.tagName).toBe("SPAN");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it.each([
    ["comment", "before <!-- hidden\nstill hidden --> after", "before after"],
    ["CDATA", "before <![CDATA[hidden\nstill hidden]]> after", "before after"],
    ["declaration", 'before <!DOCTYPE "hidden > value"> after', "before after"],
    ["processing instruction", 'before <?xml version="hidden > value"?> after', "before after"],
    ["multiline ordinary tag", 'before <span\nclass="x">visible</span> after', "before visible after"],
    ["quoted tag terminator", 'before <span title="1 > 0">visible</span> after', "before visible after"],
    ["multiline raw-text body", "before <script>\nhidden\n</script> after", "before after"],
    ["raw-text suffix", "before <style>\nhidden\n</style>visible after", "before visible after"],
  ])("strips the %s while preserving visible suffixes", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected)).toHaveTextContent(expected);
  });

  it.each([
    ["escaped", String.raw`\<span>literal\</span>`, "<span>literal</span>"],
    ["entity-encoded", "&lt;span&gt;literal&lt;/span&gt;", "<span>literal</span>"],
  ])("does not classify %s tag-shaped text as HTML", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected)).toHaveTextContent(expected);
  });
});
