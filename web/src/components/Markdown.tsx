import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

// Markdown renders GitHub-flavored markdown - change descriptions today
// (§8.6), reusable for other prose. react-markdown is safe by default: it
// builds a React element tree (never injecting raw HTML) and its default
// urlTransform drops dangerous link protocols (javascript:, etc.), so
// agent- and human-authored text is rendered without an XSS surface. The
// .markdown-body wrapper carries the typographic styling (global.css).
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={className ? `markdown-body ${className}` : "markdown-body"}>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  );
}
