import { useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Info } from "lucide-react";

import { api, unwrap, type Schemas } from "@/api/client";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const gui = {
  version: __NEXORA_VERSION__,
  commit: __NEXORA_COMMIT__,
  buildDate: __NEXORA_BUILD_DATE__,
};

function useVersion() {
  return useQuery({
    queryKey: ["version"],
    queryFn: async () => unwrap(await api.GET("/version")),
    staleTime: 60_000,
    refetchInterval: 300_000,
  });
}

/** ` · <short commit>`, linked to the repository, unless the version already is the commit tag. */
function CommitSuffix({
  version,
  commit,
  link,
}: {
  version: string;
  commit: string;
  link: string;
}) {
  const short = commit.slice(0, 7);
  if (!short || version.startsWith("sha-")) return null;
  return (
    <span className="shrink-0">
      {" · "}
      {link ? (
        <a
          href={`${link}/commit/${commit}`}
          target="_blank"
          rel="noreferrer"
          data-testid="version-footer-link"
          className="font-mono underline-offset-2 hover:text-white hover:underline"
        >
          {short}
        </a>
      ) : (
        <span data-testid="version-footer-link" className="font-mono">
          {short}
        </span>
      )}
    </span>
  );
}

/** Two build stamps differ; an unstamped one (empty) never counts as a mismatch. */
function differs(a: string | undefined, b: string): boolean {
  return !!a && !!b && a !== b;
}

/**
 * The sidebar footer: the running version with a details popover and, when the management plane
 * reports a different commit or version than this GUI build, a reload hint. Both stamps come from
 * the same image build, so either one differing means the loaded GUI is older than the server;
 * a GUI bundle built without a commit stamp is still caught by its version. On narrow screens the
 * footer text is hidden and `VersionInfoButton` in the top strip opens the same details.
 */
export function VersionFooter() {
  const q = useVersion();
  const v = q.data;
  const stale =
    differs(v?.commit, gui.commit) || differs(v?.version, gui.version);
  return (
    <div
      className={cn(
        "mt-auto shrink-0 space-y-2 px-3 pb-3 text-xs text-white/50",
        // Narrow screens show only the hint here; the details open from the top strip.
        !stale && "hidden md:block",
      )}
    >
      {stale && (
        <div
          data-testid="version-reload-hint"
          role="status"
          className="bg-sidebar-active rounded-md px-2.5 py-1.5 text-white"
        >
          New version available –{" "}
          <button
            type="button"
            onClick={() => location.reload()}
            className="text-sidebar-highlight font-medium underline-offset-2 hover:underline"
          >
            reload
          </button>
        </div>
      )}
      <div
        data-testid="version-footer"
        className="hidden items-center gap-1 px-2.5 md:flex"
      >
        <VersionDetails data={v}>
          <button
            type="button"
            className="focus-visible:ring-sidebar-highlight min-w-0 flex-1 truncate rounded-sm text-left hover:text-white focus-visible:ring-2 focus-visible:outline-none"
          >
            Nexora {v ? v.version : gui.version}
          </button>
        </VersionDetails>
        {/* The short commit sits beside the trigger: a link inside a button is not valid HTML. */}
        <CommitSuffix
          version={v ? v.version : gui.version}
          commit={v ? v.commit : gui.commit}
          link={v ? v.repository_url : ""}
        />
      </div>
    </div>
  );
}

/** The compact-layout trigger for the version details (test id `version-info-button`). */
export function VersionInfoButton() {
  const q = useVersion();
  return (
    <VersionDetails data={q.data}>
      <button
        type="button"
        aria-label="Version details"
        data-testid="version-info-button"
        className="focus-visible:ring-sidebar-highlight ml-auto inline-flex h-8 w-8 items-center justify-center rounded-md text-white/60 hover:text-white focus-visible:ring-2 focus-visible:outline-none md:hidden"
      >
        <Info className="h-4 w-4" aria-hidden="true" />
      </button>
    </VersionDetails>
  );
}

/** A click-toggled tooltip with the management plane, GUI build and engine versions. */
function VersionDetails({
  data,
  children,
}: {
  data: Schemas["VersionInfo"] | undefined;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  // Radix closes an open tooltip on pointer down, before the click: remember the state it had.
  const openAtPointerDown = useRef(false);
  return (
    <Tooltip open={open} onOpenChange={setOpen}>
      <TooltipTrigger
        asChild
        onPointerDown={() => {
          openAtPointerDown.current = open;
        }}
        onClick={(e) => {
          e.preventDefault();
          // detail 0: a keyboard activation, which has no pointer down.
          if (e.detail === 0) setOpen((o) => !o);
          else setOpen(!openAtPointerDown.current);
        }}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent
        data-testid="version-details"
        side="top"
        align="start"
        collisionPadding={8}
        className="w-72 space-y-3 text-xs leading-5"
      >
        {data && (
          <Section title="Management plane">
            <Row name="Version">{data.version}</Row>
            <Row name="Commit">
              <span className="font-mono break-all">
                {data.commit ? (
                  data.repository_url ? (
                    <a
                      href={`${data.repository_url}/commit/${data.commit}`}
                      target="_blank"
                      rel="noreferrer"
                      className="text-primary underline-offset-2 hover:underline"
                    >
                      {data.commit}
                    </a>
                  ) : (
                    data.commit
                  )
                ) : (
                  "unknown"
                )}
              </span>
            </Row>
            <Row name="Built">{data.build_date || "unknown"}</Row>
          </Section>
        )}
        <Section title="GUI build">
          <Row name="Version">{gui.version}</Row>
          <Row name="Commit">
            <span className="font-mono break-all">
              {gui.commit || "unknown"}
            </span>
          </Row>
          <Row name="Built">{gui.buildDate || "unknown"}</Row>
        </Section>
        {data && (
          <Section title="Engines">
            {data.engines.length === 0 ? (
              <p className="text-muted-foreground">
                No engine reports a version.
              </p>
            ) : (
              data.engines.map((e) => (
                <p key={e.version} className="font-mono">
                  {e.count} × {e.version}
                </p>
              ))
            )}
            {data.engines.length > 1 && (
              <p className="text-warning">Engines run different versions</p>
            )}
          </Section>
        )}
      </TooltipContent>
    </Tooltip>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <p className="font-medium">{title}</p>
      {children}
    </div>
  );
}

function Row({ name, children }: { name: string; children: ReactNode }) {
  return (
    <p className="flex gap-2">
      <span className="text-muted-foreground w-14 shrink-0">{name}</span>
      <span className="min-w-0">{children}</span>
    </p>
  );
}
