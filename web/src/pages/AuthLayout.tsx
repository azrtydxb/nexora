import type { ReactNode } from "react";

import { Wordmark } from "@/components/layout/AppShell";

/** The frame of the signed-out screens: brand panel beside a narrow form column. */
export function AuthLayout({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <div className="grid min-h-screen md:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
      <section className="bg-sidebar text-sidebar-foreground relative hidden flex-col justify-between overflow-hidden p-10 md:flex">
        <Wordmark />
        <ResolverField />
        <p className="relative max-w-sm text-sm leading-relaxed">
          Management for the Nexora DNS resolver fleet: upstreams, filtering,
          access control and every change on record.
        </p>
      </section>
      <main className="flex items-center justify-center px-4 py-12">
        <div className="w-full max-w-sm">
          <div className="bg-sidebar mb-8 inline-flex rounded-md px-3 py-2 md:hidden">
            <Wordmark />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
          <p className="text-muted-foreground mt-1.5 mb-8 text-sm">
            {description}
          </p>
          {children}
        </div>
      </main>
    </div>
  );
}

/** A quiet diagram of queries fanning out from engines to upstreams. */
function ResolverField() {
  const engines = [70, 150, 230];
  const upstreams = [40, 110, 190, 260];
  return (
    <svg
      viewBox="0 0 320 300"
      className="text-sidebar-highlight my-8 w-full max-w-md"
      fill="none"
      aria-hidden
    >
      {engines.map((ey) =>
        upstreams.map((uy) => (
          <path
            key={`${ey}-${uy}`}
            d={`M60 ${ey} C 160 ${ey}, 160 ${uy}, 260 ${uy}`}
            stroke="currentColor"
            strokeOpacity={0.14}
            strokeWidth={1}
          />
        )),
      )}
      {engines.map((y) => (
        <rect
          key={y}
          x={44}
          y={y - 12}
          width={24}
          height={24}
          rx={5}
          fill="currentColor"
          fillOpacity={0.9}
        />
      ))}
      {upstreams.map((y) => (
        <circle
          key={y}
          cx={266}
          cy={y}
          r={6}
          stroke="currentColor"
          strokeOpacity={0.6}
          strokeWidth={1.5}
        />
      ))}
    </svg>
  );
}
