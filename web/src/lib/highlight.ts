// Syntax highlighting for the code browser (BrowsePage's code + blame
// views). lowlight = highlight.js emitting a hast tree instead of an HTML
// string, which is what makes LINE-SAFE rendering possible: hljs tokens
// (strings, comments) freely span newlines, so the tree is flattened to
// (class, text) tokens and split at "\n" with each segment keeping its
// class - no innerHTML anywhere, every byte renders through React text
// nodes exactly like the unhighlighted path.
//
// Grammars are REGISTERED, not auto-detected: detection over every grammar
// is slow and wrong often enough to be worse than plain text, and explicit
// registration is also what keeps the (lazily loaded - see useHighlighter)
// chunk bounded. The module is imported only via dynamic import() so the
// main bundle does not carry highlight.js at all.
import { createLowlight } from "lowlight";
import type { Root, RootContent } from "hast";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import makefile from "highlight.js/lib/languages/makefile";
import markdown from "highlight.js/lib/languages/markdown";
import protobuf from "highlight.js/lib/languages/protobuf";
import python from "highlight.js/lib/languages/python";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";

const lowlight = createLowlight({
  bash,
  css,
  dockerfile,
  go,
  ini,
  javascript,
  json,
  makefile,
  markdown,
  protobuf,
  python,
  sql,
  typescript,
  xml,
  yaml,
});

// Extension -> registered grammar. Deliberately conservative: an unmapped
// extension renders plain rather than mis-highlighted.
const extToLang: Record<string, string> = {
  bash: "bash",
  bzl: "python", // Starlark: python's grammar is the accepted stand-in
  cjs: "javascript",
  css: "css",
  go: "go",
  html: "xml",
  ini: "ini",
  js: "javascript",
  json: "json",
  jsx: "javascript",
  md: "markdown",
  mjs: "javascript",
  proto: "protobuf",
  py: "python",
  sh: "bash",
  sql: "sql",
  svg: "xml",
  toml: "ini",
  ts: "typescript",
  tsx: "typescript",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
};

// Well-known extensionless basenames.
const nameToLang: Record<string, string> = {
  BUILD: "python",
  "BUILD.bazel": "python",
  Dockerfile: "dockerfile",
  Makefile: "makefile",
  "MODULE.bazel": "python",
  "WORKSPACE.bazel": "python",
};

/** The registered grammar for a repo path, or undefined to render plain. */
export function languageFor(path: string): string | undefined {
  const base = path.split("/").pop() ?? path;
  if (nameToLang[base]) return nameToLang[base];
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return undefined; // extensionless or dotfile
  return extToLang[base.slice(dot + 1).toLowerCase()];
}

/** One highlighted span: text plus the hljs-* class stack it renders with. */
export type Token = { text: string; cls?: string };

// Highlighting is display sugar - a pathological file must never hang the
// browser. Past these bounds the caller renders plain text.
const maxBytes = 512 * 1024;
const maxLines = 10_000;

/**
 * Highlight content into per-line token arrays, or null when the path has
 * no registered grammar, the file is too large, or the grammar throws -
 * every null renders exactly what the browser rendered before this
 * feature existed.
 */
export function highlightLines(content: string, path: string): Token[][] | null {
  const lang = languageFor(path);
  if (!lang || content.length > maxBytes) return null;

  let tree: Root;
  try {
    tree = lowlight.highlight(lang, content);
  } catch {
    return null;
  }

  const flat: Token[] = [];
  const walk = (nodes: RootContent[], cls: string) => {
    for (const n of nodes) {
      if (n.type === "text") {
        flat.push({ text: n.value, cls: cls || undefined });
      } else if (n.type === "element") {
        const own = Array.isArray(n.properties?.className)
          ? n.properties.className.join(" ")
          : "";
        walk(n.children as RootContent[], cls ? `${cls} ${own}` : own);
      }
    }
  };
  walk(tree.children as RootContent[], "");

  // Split the token stream at newlines; a token spanning lines contributes
  // one segment per line, each keeping the token's class.
  const lines: Token[][] = [[]];
  for (const t of flat) {
    const parts = t.text.split("\n");
    parts.forEach((part, i) => {
      if (i > 0) {
        if (lines.length >= maxLines) return;
        lines.push([]);
      }
      if (part !== "") lines[lines.length - 1]!.push({ text: part, cls: t.cls });
    });
  }
  if (lines.length >= maxLines) return null;
  return lines;
}
