import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

// renderToStaticMarkup is DOM-free, so these run under the repo's plain
// (non-jsdom) vitest setup and also serve as the rendering verification: the
// Markdown wrapper genuinely turns markdown into HTML and stays XSS-safe.
const render = (text: string) => renderToStaticMarkup(createElement(Markdown, { text }));

describe("Markdown", () => {
  it("renders GitHub-flavored markdown to HTML elements", () => {
    const html = render("**bold** and `code`\n\n- one\n- two\n\n[link](https://example.com)");
    expect(html).toContain("markdown-body");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<code>code</code>");
    expect(html).toContain("<li>one</li>");
    expect(html).toContain('href="https://example.com"');
  });

  it("renders a GFM table (remark-gfm is wired)", () => {
    const html = render("| a | b |\n| - | - |\n| 1 | 2 |");
    expect(html).toContain("<table>");
    expect(html).toContain("<td>1</td>");
  });

  it("does not emit raw HTML from the source (safe by default)", () => {
    const html = render("<script>alert(1)</script> hello");
    expect(html).not.toContain("<script>");
    expect(html).toContain("hello");
  });

  it("drops dangerous link protocols", () => {
    const html = render("[x](javascript:alert(1))");
    expect(html).not.toContain("javascript:alert");
  });
});
