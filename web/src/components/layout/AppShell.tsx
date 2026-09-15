import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";
import { useQuery } from "@tanstack/react-query";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router";
import {
  ArrowLeftRight,
  BadgeCheck,
  ChevronDown,
  ChevronRight,
  Cpu,
  Funnel,
  Globe,
  KeyRound,
  KeySquare,
  LayoutDashboard,
  LifeBuoy,
  ListX,
  LogOut,
  Moon,
  ScrollText,
  Server,
  Settings,
  ShieldBan,
  ShieldCheck,
  ShieldHalf,
  Sun,
  Tags,
  TextSearch,
  UserRound,
  Users,
  type LucideIcon,
} from "lucide-react";

import { api } from "@/api/client";
import { useCurrentUser, useLogout } from "@/auth/AuthProvider";
import { roleCan, type OperationId, type Role } from "@/auth/permissions";
import { ChangePasswordDialog } from "@/components/ChangePasswordDialog";
import { SavedNote } from "@/components/common";
import { VersionFooter, VersionInfoButton } from "@/components/VersionFooter";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

type NavLeaf = {
  route: string;
  path: string;
  label: string;
  icon: LucideIcon;
  /** The screen's list operation; absent for a screen every signed-in user may open (Help). */
  op?: OperationId;
  /** Active only on the exact path, for an item with a sibling item below its path. */
  end?: boolean;
};

type NavParent = {
  route: string;
  label: string;
  icon: LucideIcon;
  children: NavLeaf[];
};

type NavItem = NavLeaf | NavParent;

const isParent = (item: NavItem): item is NavParent => "children" in item;

const leafAllowed = (role: Role | undefined, leaf: NavLeaf) =>
  role !== undefined && (leaf.op === undefined || roleCan(role, leaf.op));

/** True when the pathname is the leaf's screen or a screen below it. */
function onLeaf(pathname: string, leaf: NavLeaf) {
  if (leaf.path === "/") return pathname === "/";
  return pathname === leaf.path || pathname.startsWith(`${leaf.path}/`);
}

const FILTERING_NAV_KEY = "nexora-nav-filtering";

// Each item shows when the user may call the screen's list operation.
const navGroups: { label: string; items: NavItem[] }[] = [
  {
    label: "Overview",
    items: [
      {
        route: "dashboard",
        path: "/",
        label: "Dashboard",
        icon: LayoutDashboard,
        op: "getDashboard",
      },
      {
        route: "query-log",
        path: "/query-log",
        label: "Query log",
        icon: TextSearch,
        op: "searchQueryLog",
      },
      {
        route: "help",
        path: "/help",
        label: "Help",
        icon: LifeBuoy,
      },
    ],
  },
  {
    label: "Resolver",
    items: [
      {
        route: "resolution",
        path: "/resolution",
        label: "Forwarding & recursion",
        icon: Server,
        op: "listUpstreams",
      },
      {
        route: "filtering-group",
        label: "Filtering",
        icon: Funnel,
        children: [
          {
            route: "filtering",
            path: "/filtering",
            label: "Blocklist / allowlist",
            icon: ListX,
            op: "listFilterLists",
            end: true,
          },
          {
            route: "filter-categories",
            path: "/filtering/categories",
            label: "Categories",
            icon: Tags,
            op: "listFilterCategories",
          },
          {
            route: "policies",
            path: "/policies",
            label: "Policies",
            icon: ShieldHalf,
            op: "listPolicyGroups",
          },
          {
            route: "rpz",
            path: "/rpz",
            label: "RPZ",
            icon: ShieldBan,
            op: "listRpzZones",
          },
        ],
      },
      {
        route: "rewrites",
        path: "/rewrites",
        label: "Rewrites",
        icon: ArrowLeftRight,
        op: "listRewrites",
      },
      {
        route: "dnssec",
        path: "/dnssec",
        label: "DNSSEC",
        icon: BadgeCheck,
        op: "getDnssecSettings",
      },
      {
        route: "zones",
        path: "/zones",
        label: "Zones",
        icon: Globe,
        op: "listZones",
      },
      {
        route: "access-control",
        path: "/access-control",
        label: "Access control",
        icon: ShieldCheck,
        op: "getAccessControl",
      },
      {
        route: "settings",
        path: "/settings",
        label: "Settings",
        icon: Settings,
        op: "getResolverSettings",
      },
    ],
  },
  {
    label: "Fleet",
    items: [
      {
        route: "engines",
        path: "/engines",
        label: "Engines",
        icon: Cpu,
        op: "listEngines",
      },
    ],
  },
  {
    label: "Administration",
    items: [
      {
        route: "users",
        path: "/users",
        label: "Users",
        icon: Users,
        op: "listUsers",
      },
      {
        route: "api-tokens",
        path: "/api-tokens",
        label: "API tokens",
        icon: KeyRound,
        op: "listApiTokens",
      },
      {
        route: "audit",
        path: "/audit",
        label: "Audit log",
        icon: ScrollText,
        op: "listAuditEvents",
      },
    ],
  },
];

export function AppShell() {
  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="bg-background/85 sticky top-0 z-30 flex h-14 items-center justify-end gap-3 border-b px-4 backdrop-blur md:px-8">
          <HealthBadge />
          <UserMenu />
        </header>
        <main className="flex-1 px-4 py-6 md:px-8 md:py-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function Sidebar() {
  return (
    <aside className="bg-sidebar text-sidebar-foreground flex shrink-0 flex-col md:sticky md:top-0 md:h-screen md:w-60">
      <div className="flex h-14 items-center gap-2.5 px-5">
        <Wordmark />
        <VersionInfoButton />
      </div>
      <nav
        className="flex gap-4 overflow-x-auto px-3 pb-3 md:min-h-0 md:flex-col md:gap-5 md:pt-4"
        aria-label="Main"
      >
        {navGroups.map((g) => (
          <NavGroup key={g.label} label={g.label} items={g.items} />
        ))}
      </nav>
      <VersionFooter />
    </aside>
  );
}

function NavGroup({ label, items }: { label: string; items: NavItem[] }) {
  const { user } = useCurrentUser();
  // A group whose every screen is out of the user's reach (Administration for non-admins) is hidden.
  const leaves = items.flatMap((i) => (isParent(i) ? i.children : [i]));
  if (!leaves.some((l) => leafAllowed(user?.role, l))) return null;
  return (
    <div className="flex items-center gap-1 md:flex-col md:items-stretch">
      <div className="hidden px-2 pb-1 text-xs font-medium text-white/40 md:block">
        {label}
      </div>
      {items.map((item) =>
        isParent(item) ? (
          <NavParentEntry key={item.route} item={item} />
        ) : (
          <NavEntry key={item.route} item={item} />
        ),
      )}
    </div>
  );
}

function readFilteringOpen() {
  try {
    return localStorage.getItem(FILTERING_NAV_KEY) !== "closed";
  } catch {
    return true;
  }
}

const navItemClass = (active: boolean) =>
  cn(
    "group relative flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm whitespace-nowrap transition-colors",
    "focus-visible:ring-sidebar-highlight focus-visible:ring-2 focus-visible:outline-none",
    active
      ? "bg-sidebar-active text-white"
      : "hover:bg-sidebar-active/60 hover:text-white",
  );

/** A collapsible parent (Filtering): open by default, remembered, and opened on a child route. */
function NavParentEntry({ item }: { item: NavParent }) {
  const { user } = useCurrentUser();
  const { pathname } = useLocation();
  const onChild = item.children.some((c) => onLeaf(pathname, c));
  const [open, setOpen] = useState(() => onChild || readFilteringOpen());
  // Entering a child route (a link elsewhere, the address bar) opens the group; the user may
  // still collapse it there.
  const [wasOnChild, setWasOnChild] = useState(onChild);
  if (onChild !== wasOnChild) {
    setWasOnChild(onChild);
    if (onChild) setOpen(true);
  }
  if (!item.children.some((c) => leafAllowed(user?.role, c))) return null;

  const toggle = () => {
    const next = !open;
    setOpen(next);
    try {
      localStorage.setItem(FILTERING_NAV_KEY, next ? "open" : "closed");
    } catch {
      // Storage unavailable (private mode, quota): the state lasts for this page only.
    }
  };
  const Icon = item.icon;
  const listId = `nav-${item.route}-children`;
  return (
    <>
      <button
        type="button"
        data-testid={`nav-${item.route}`}
        aria-expanded={open}
        aria-controls={listId}
        onClick={toggle}
        className={cn(navItemClass(onChild && !open), "text-left")}
      >
        <Icon
          className={cn(
            "h-4 w-4",
            onChild ? "text-sidebar-highlight" : "opacity-70",
          )}
        />
        <span className={cn("flex-1", onChild && "text-white")}>
          {item.label}
        </span>
        <ChevronRight
          aria-hidden
          className={cn(
            "h-3.5 w-3.5 opacity-60 transition-transform",
            open && "rotate-90",
          )}
        />
      </button>
      <div
        id={listId}
        className={cn(
          "items-center gap-1 md:flex-col md:items-stretch md:pl-4",
          open ? "flex" : "hidden",
        )}
      >
        {item.children.map((c) => (
          <NavEntry key={c.route} item={c} />
        ))}
      </div>
    </>
  );
}

function NavEntry({ item }: { item: NavLeaf }) {
  const { user } = useCurrentUser();
  if (!leafAllowed(user?.role, item)) return null;
  const Icon = item.icon;
  return (
    <NavLink
      to={item.path}
      end={item.path === "/" || item.end}
      data-testid={`nav-${item.route}`}
      className={({ isActive }) => navItemClass(isActive)}
    >
      {({ isActive }) => (
        <>
          <span
            aria-hidden
            className={cn(
              "bg-sidebar-highlight absolute top-1.5 bottom-1.5 left-0 hidden w-0.5 rounded-full md:block",
              isActive ? "opacity-100" : "opacity-0",
            )}
          />
          <Icon
            className={cn(
              "h-4 w-4",
              isActive ? "text-sidebar-highlight" : "opacity-70",
            )}
          />
          {item.label}
        </>
      )}
    </NavLink>
  );
}

/** The Nexora mark: a resolver node fanning out to three upstreams. */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cn("flex items-center gap-2.5 text-white", className)}>
      <svg
        viewBox="0 0 24 24"
        className="text-sidebar-highlight h-6 w-6"
        fill="none"
        aria-hidden
      >
        <circle cx="5" cy="12" r="2.6" fill="currentColor" />
        <path
          d="M7.5 12h4.5M12 12l6-6.5M12 12h7M12 12l6 6.5"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
        <circle cx="19" cy="5.5" r="1.6" fill="currentColor" opacity=".55" />
        <circle cx="20" cy="12" r="1.6" fill="currentColor" opacity=".55" />
        <circle cx="19" cy="18.5" r="1.6" fill="currentColor" opacity=".55" />
      </svg>
      <span className="text-[15px] font-semibold tracking-tight">Nexora</span>
    </span>
  );
}

function HealthBadge() {
  const q = useQuery({
    queryKey: ["health"],
    queryFn: async () => {
      const r = await api.GET("/health");
      // 503 carries the same Health body, reporting which dependency is down.
      return r.data ?? r.error ?? null;
    },
    refetchInterval: 15_000,
    retry: false,
  });
  const ok = q.data?.status === "ok";
  const title = q.data
    ? `Management API ${q.data.status}, database ${q.data.database}, version ${q.data.version}`
    : "Management API unreachable";
  if (q.isPending) return null;
  return (
    <span
      data-testid="health-badge"
      title={title}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-medium",
        ok
          ? "border-success/30 text-success"
          : "border-warning/40 text-warning",
      )}
    >
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          ok ? "bg-success" : "bg-warning",
        )}
      />
      {ok ? "ok" : "degraded"}
    </span>
  );
}

function UserMenu() {
  const { user } = useCurrentUser();
  const logout = useLogout();
  const [theme, toggleTheme] = useTheme();
  const [open, setOpen] = useState(false);
  const [changingPassword, setChangingPassword] = useState(false);
  const [passwordChanged, setPasswordChanged] = useState(false);
  const navigate = useNavigate();
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!passwordChanged) return;
    const t = window.setTimeout(() => setPasswordChanged(false), 5000);
    return () => window.clearTimeout(t);
  }, [passwordChanged]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  if (!user) return null;
  return (
    <div className="relative flex items-center gap-3" ref={ref}>
      <SavedNote show={passwordChanged}>Password changed</SavedNote>
      <button
        type="button"
        data-testid="user-menu"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="hover:bg-accent focus-visible:ring-ring flex items-center gap-2 rounded-md py-1 pr-2 pl-1 text-sm focus-visible:ring-2 focus-visible:outline-none"
      >
        <span className="bg-primary text-primary-foreground flex h-7 w-7 items-center justify-center rounded-full text-xs font-semibold uppercase">
          {user.username.slice(0, 1)}
        </span>
        <span className="font-medium">{user.username}</span>
        <span className="text-muted-foreground hidden sm:inline">
          {user.role}
        </span>
        <ChevronDown className="text-muted-foreground h-3.5 w-3.5" />
      </button>
      {open && (
        <div
          role="menu"
          className="bg-popover text-popover-foreground absolute top-full right-0 z-40 mt-1.5 w-56 rounded-md border p-1 shadow-lg"
        >
          <div className="px-2.5 py-2">
            <div className="truncate text-sm font-medium">{user.username}</div>
            <div className="text-muted-foreground truncate text-xs">
              {user.email}
            </div>
          </div>
          <div className="bg-border -mx-1 my-1 h-px" />
          <MenuItem
            data-testid="menu-profile"
            onClick={() => {
              setOpen(false);
              navigate("/account");
            }}
          >
            <UserRound className="h-4 w-4" />
            Profile
          </MenuItem>
          {user.source === "local" && (
            <MenuItem
              data-testid="menu-change-password"
              onClick={() => {
                setOpen(false);
                setChangingPassword(true);
              }}
            >
              <KeySquare className="h-4 w-4" />
              Change password
            </MenuItem>
          )}
          <MenuItem onClick={toggleTheme}>
            {theme === "dark" ? (
              <Sun className="h-4 w-4" />
            ) : (
              <Moon className="h-4 w-4" />
            )}
            {theme === "dark" ? "Light theme" : "Dark theme"}
          </MenuItem>
          <MenuItem data-testid="logout" onClick={() => void logout()}>
            <LogOut className="h-4 w-4" />
            Sign out
          </MenuItem>
        </div>
      )}
      {changingPassword && (
        <ChangePasswordDialog
          onClose={() => setChangingPassword(false)}
          onDone={() => setPasswordChanged(true)}
        />
      )}
    </div>
  );
}

function MenuItem(props: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="button"
      role="menuitem"
      {...props}
      className="hover:bg-accent focus-visible:bg-accent flex w-full items-center gap-2 rounded-sm px-2.5 py-1.5 text-left text-sm focus-visible:outline-none"
    />
  );
}

/** The title row every screen opens with. */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  useEffect(() => {
    document.title = `${title} · Nexora`;
  }, [title]);
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && (
          <p className="text-muted-foreground mt-1 max-w-prose text-sm">
            {description}
          </p>
        )}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
