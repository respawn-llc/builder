import { StrictMode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StaticMarkdown, StreamingMarkdown, TaskBodyMarkdown } from "@/ui";

function taskBodyText(expected: string): HTMLElement {
  return screen.getByText(expected, { selector: "span" });
}

function renderedText(view: ReturnType<typeof render>): string {
  return view.container.textContent;
}

function codeText(view: ReturnType<typeof render>): string[] {
  return Array.from(view.container.getElementsByTagName("code"), (element) => element.textContent);
}

describe("Markdown adapters", () => {
  it("exposes static, streaming, and task-body adapters", () => {
    const view = render(
      <>
        <StaticMarkdown value="static value" />
        <StreamingMarkdown value="streaming value" />
        <TaskBodyMarkdown value="task-body value" />
      </>,
    );

    expect(screen.getByText("static value")).toBeInTheDocument();
    expect(renderedText(view)).toBe("static valuestreaming valuetask-body value");
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
    const view = render(<StreamingMarkdown value="accumulated value" />);

    await waitFor(() => {
      expect(renderedText(view)).toBe("accumulated value");
    });
  });

  it("renders GFM tables and strikethrough through Streamdown defaults", async () => {
    render(<StaticMarkdown value={"| Name |\n| --- |\n| ~~done~~ |"} />);

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getByText("done")).toBeInTheDocument();
  });

  it("renders streaming output through the public adapter boundary", async () => {
    const view = render(<StreamingMarkdown value="stream" />);

    await waitFor(() => {
      expect(renderedText(view)).toBe("stream");
    });
  });

  it("uses Streamdown's default sanitized HTML and link behavior", async () => {
    render(
      <StaticMarkdown
        value={
          'before <span title="valid title">inner</span> [safe](https://example.com) [unsafe](javascript:alert(1)) [data-unsafe](data:text/html,blocked)'
        }
      />,
    );

    expect(await screen.findByText("inner")).toBeInTheDocument();
    expect(screen.getByTitle("valid title")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "safe" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "unsafe" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "data-unsafe" })).not.toBeInTheDocument();
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

  it.each(["javascript", "js", "JavaScript", "JS", "python", "py", "Python", "PY"])(
    "highlights completed %s code",
    async (language) => {
      const view = render(<StaticMarkdown value={`\`\`\`${language}\nconst answer = 1;\n\`\`\``} />);

      await waitFor(() => {
        expect(codeText(view)).toEqual(["const answer = 1;\n"]);
      });
    },
  );

  it.each(["JS", "PY"])("keeps mixed-case incomplete %s code plain", async (language) => {
    render(<StreamingMarkdown value={`\`\`\`${language}\nconst answer = 1;`} />);

    expect(await screen.findByText("const answer = 1;")).toBeInTheDocument();
    expect(screen.queryByText("const", { exact: true })).not.toBeInTheDocument();
  });

  it("keeps collision-shaped completed blocks exact", async () => {
    const view = render(
      <StaticMarkdown
        value={"```javascript\nconst first = 1;\n```\n\n```javascript\nconst second = 2;\n```"}
      />,
    );

    await waitFor(() => {
      expect(codeText(view)).toEqual(["const first = 1;\n", "const second = 2;\n"]);
    });
  });

  it("keeps canonical and alias highlighting stable under StrictMode", async () => {
    const view = render(
      <StrictMode>
        <StreamingMarkdown
          value={"```javascript\nconst canonical = 1;\n```\n\n```js\nconst alias = 2;\n```"}
        />
      </StrictMode>,
    );

    await waitFor(() => {
      expect(codeText(view)).toEqual(["const canonical = 1;\n", "const alias = 2;\n"]);
    });
  });

  it("shows the exact current live text after appending prose", async () => {
    const view = render(<StreamingMarkdown value={"```javascript\nconst answer = 1;\n```\n\ntail-A"} />);

    await waitFor(() => {
      expect(renderedText(view)).toBe("const answer = 1;\ntail-A");
    });

    view.rerender(<StreamingMarkdown value={"```javascript\nconst answer = 1;\n```\n\ntail-A tail-B"} />);

    await waitFor(() => {
      expect(renderedText(view)).toBe("const answer = 1;\ntail-A tail-B");
    });
  });
});

describe("TaskBodyMarkdown", () => {
  it.each([
    ["punctuation and formatting", "5 * 3 | 2\n\n**bold** *italic* ~~gone~~", "5 * 3 | 2 bold italic gone"],
    [
      "autolinks and reference labels",
      "<https://example.com> [label][ref]\n\n[ref]: https://example.com",
      "https://example.com label",
    ],
    ["literal angle punctuation", "love <3 this\n\nresult <- value", "love <3 this result <- value"],
    ["table cells", "| Front | Back |\n| --- | --- |\n| One | Two |", "Front Back One Two"],
    ["image alt text", "![image alt](image.png)", "image alt"],
    ["code payloads", "inline `x`\n\n```javascript\nconst x = 1;\n```", "inline x const x = 1;"],
  ])("projects %s to readable plain text", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected).textContent).toBe(expected);
  });

  it("keeps the server-bounded value intact and renders one plain span", () => {
    const value = "x".repeat(512);
    render(<TaskBodyMarkdown value={value} />);

    const element = taskBodyText(value);
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
    ["raw HTML block", "<div>visible</div>", "visible"],
    ["raw-text block suffix", "<script>hidden</script>visible", "visible"],
    ["CRLF raw-text suffix", "<script>hidden\r\n</script>\r\nvisible", "visible"],
  ])("strips the %s while preserving visible suffixes", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected).textContent).toBe(expected);
  });

  it.each([
    ["escaped", String.raw`\<span>literal\</span>`, "<span>literal</span>"],
    ["entity-encoded", "&lt;span&gt;literal&lt;/span&gt;", "<span>literal</span>"],
    ["unterminated ordinary tag", "use <value before saving", "use <value before saving"],
    ["invalid declaration", "if a <! b > c", "if a <! b > c"],
  ])("does not classify %s tag-shaped text as HTML", (_name, value, expected) => {
    render(<TaskBodyMarkdown value={value} />);

    expect(taskBodyText(expected).textContent).toBe(expected);
  });
});
