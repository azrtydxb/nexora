import { Children, isValidElement, useEffect, type ReactNode } from "react";
import { Link, Navigate, useLocation, useParams } from "react-router";
import Markdown, { type Components } from "react-markdown";

import { PageHeader } from "@/components/layout/AppShell";
import { Card } from "@/components/ui/card";
import { topics } from "@/help/topics";

function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (isValidElement<{ children?: ReactNode }>(node))
    return textOf(node.props.children);
  return Children.toArray(node).map(textOf).join("");
}

/** The anchor of a heading: "QNAME minimisation" -> "qname-minimisation". */
function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}

const components: Components = {
  // The topic's title is the page header; the file's own "# Title" is not repeated.
  h1: () => null,
  h2: ({ children }) => (
    <h2 id={slug(textOf(children))} className="scroll-mt-4">
      {children}
    </h2>
  ),
  a: ({ href, children }) =>
    href?.startsWith("/") ? (
      <Link to={href}>{children}</Link>
    ) : (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    ),
};

/** Route element for `/help` (topic list) and `/help/:topic` (one markdown topic). */
export default function HelpPage() {
  const { topic: key } = useParams();
  const { hash } = useLocation();
  const topic =
    key === undefined ? undefined : topics.find((t) => t.key === key);

  useEffect(() => {
    if (!topic || hash.length < 2) return;
    document
      .getElementById(decodeURIComponent(hash.slice(1)))
      ?.scrollIntoView();
  }, [topic, hash]);

  if (key !== undefined && !topic) return <Navigate to="/help" replace />;

  if (!topic) {
    return (
      <>
        <PageHeader
          title="Help"
          description="How each part of Nexora works, what its settings do, and what changing them affects."
        />
        <Card className="divide-y" data-testid="help-topics">
          {topics.map((t) => (
            <Link
              key={t.key}
              to={`/help/${t.key}`}
              data-testid={`help-topic-${t.key}`}
              className="hover:bg-muted/50 block px-4 py-3 text-sm font-medium"
            >
              {t.title}
            </Link>
          ))}
        </Card>
      </>
    );
  }

  return (
    <>
      <PageHeader title={topic.title} />
      <p className="mb-4 text-sm">
        <Link to="/help" className="text-primary hover:underline">
          All help topics
        </Link>
      </p>
      <article
        data-testid="help-article"
        className="max-w-prose text-sm leading-6 break-words [&_a]:text-primary [&_a]:underline-offset-2 hover:[&_a]:underline [&_code]:font-mono [&_code]:text-[13px] [&_h2]:mt-6 [&_h2]:mb-2 [&_h2]:text-lg [&_h2]:font-semibold [&_li]:mt-1 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:mt-2 [&_ul]:list-disc [&_ul]:pl-5"
      >
        <Markdown components={components}>{topic.body}</Markdown>
      </article>
    </>
  );
}
