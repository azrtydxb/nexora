import type { HelpArea } from "./types";

export const adminHelp: HelpArea = {
  pages: [
    "pages/UsersPage.tsx",
    "pages/ApiTokensPage.tsx",
    "pages/AuditPage.tsx",
    "pages/AccountPage.tsx",
    "components/ChangePasswordDialog.tsx",
    "pages/QueryLogPage.tsx",
    "pages/DashboardPage.tsx",
    "pages/LoginPage.tsx",
    "pages/SetupPage.tsx",
  ],
  entries: {
    // Sign-in and first-run setup: no "Learn more" link, help pages need a signed-in user.
    username: {
      text: "Your Nexora username. Users from an identity provider sign in with single sign-on instead.",
    },
    password: {
      text: "The password of your Nexora account. More than 10 failed sign-ins in 15 minutes pause further attempts.",
    },
    "setup-token": {
      text: "The one-time token nexora-mgmt writes to its log the first time it starts with an empty database. It works once; if the log is gone, create the admin with nexora-mgmt user create.",
    },
    "setup-username": {
      text: "The username of the first administrator. It cannot be changed later.",
    },
    "setup-email": {
      text: "The administrator's email address, shown on the Users page.",
    },
    "setup-password": {
      text: "The administrator's password.",
      range: "12 characters or more",
    },

    // Users
    "user-username": {
      text: "The name this person signs in with. It cannot be changed later.",
      range: "Up to 64 characters",
      topic: "users",
      anchor: "roles",
    },
    "user-email": {
      text: "Contact address shown in the user list. Optional.",
      topic: "users",
      anchor: "roles",
    },
    "user-password": {
      text: "The sign-in password for a local user. When editing, leave it empty to keep the current password.",
      range: "12 characters or more",
      effect: "Setting a new password signs the user out everywhere.",
      topic: "users",
      anchor: "roles",
    },
    "user-role": {
      text: "What the user may do. Viewer reads everything except users, API tokens and the audit log; operator also changes DNS configuration; admin can do everything.",
      default: "viewer",
      effect:
        "Takes effect on the user's next request. The last active admin cannot lose the admin role or be disabled.",
      topic: "users",
      anchor: "roles",
    },
    "user-disabled": {
      text: "A disabled user cannot sign in, and their API tokens are refused. The account and its tokens stay in place for when it is enabled again.",
      default: "Off",
      effect: "Turning it on signs the user out everywhere.",
      topic: "users",
      anchor: "roles",
    },

    // API tokens
    "token-name": {
      text: "A name that tells you what uses the token, such as ci-deploy. It appears in the audit log as the actor.",
      range: "Up to 64 characters",
      topic: "users",
      anchor: "api-tokens",
    },
    "token-role": {
      text: "What requests with this token may do. It can never exceed your own role.",
      default: "viewer",
      topic: "users",
      anchor: "api-tokens",
    },
    "token-expiry": {
      text: "When the token stops working. Revoking it stops it at once, whatever the expiry.",
      default: "90 days",
      topic: "users",
      anchor: "api-tokens",
    },

    // Audit log
    "audit-col-version": {
      text: "The configuration version this change published. Empty for changes that publish nothing, such as users and API tokens.",
      topic: "users",
      anchor: "roles",
    },

    // Account
    "account-email": {
      text: "Your contact address. Users from an identity provider change it there.",
      topic: "users",
      anchor: "account",
    },
    "account-display-name": {
      text: "The name shown for you instead of your username. Users from an identity provider change it there.",
      range: "Up to 64 characters",
      topic: "users",
      anchor: "account",
    },
    "account-theme": {
      text: "Light, dark, or follow your device's setting.",
      default: "System",
      topic: "users",
      anchor: "account",
    },
    "account-time-zone": {
      text: "The IANA time zone, such as Europe/Brussels, used for the times Nexora shows you.",
      default: "Empty (your browser's time zone)",
      topic: "users",
      anchor: "account",
    },
    "account-clock-24h": {
      text: "Show times as 14:05 instead of 2:05 PM.",
      default: "Off",
      topic: "users",
      anchor: "account",
    },
    "account-querylog-live": {
      text: "Whether the query log opens in live mode, reloading the newest queries every 5 seconds.",
      default: "On",
      topic: "users",
      anchor: "account",
    },
    "password-current": {
      text: "Your password today, to prove it is you. More than 10 failed attempts in 15 minutes pause further changes.",
      topic: "users",
      anchor: "account",
    },
    "password-new": {
      text: 'At least 12 characters and different from your current password. Your other signed-in sessions end when "Sign out my other sessions" is on.',
      range: "12 characters or more",
      topic: "users",
      anchor: "account",
    },
    "password-confirm": {
      text: "Type the new password again to catch typing mistakes.",
      topic: "users",
      anchor: "account",
    },
    "password-revoke-others": {
      text: "Ends your sessions in other browsers and devices. This browser stays signed in.",
      default: "On",
      topic: "users",
      anchor: "account",
    },

    // Query log
    "querylog-live": {
      text: "Reloads the newest page every 5 seconds while it is on. Paging to older results pauses it.",
      default: "Your account's query log live setting (on)",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-name": {
      text: 'Matches part of a query name, ignoring case and a trailing dot: "tube" finds youtube.com and www.youtube.com.',
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-client": {
      text: "The exact IP address of the client that sent the query, such as 192.0.2.10.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-qtype": {
      text: "The record type asked for. Several values match any of them; none matches every type.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-rcode": {
      text: "The response code sent back: NOERROR, NXDOMAIN (name does not exist), SERVFAIL (resolution failed), REFUSED or FORMERR. Several values match any of them.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-cache": {
      text: "Where the answer came from: hit (cache), miss (resolved now), stale (an expired cache entry served because resolution failed), auth (a zone Nexora hosts) or none (no cache lookup, such as a blocked or refused query).",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-filter": {
      text: "The filtering decision: blocked, allowed by the allowlist, rewritten, or none when no filter rule matched.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-category": {
      text: "The filter category whose source blocked the query. Several values match any of them.",
      topic: "filtering",
      anchor: "categories-and-licenses",
    },
    "querylog-source": {
      text: "Why a query was decided: a blocklist subscription, a filter category source, the allowlist, a response policy zone, a rewrite, or an access control refusal. Several values match any of them.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-policy-group": {
      text: "The policy group whose rules applied to the client. Global means the client is in no policy group.",
      topic: "filtering",
      anchor: "policies",
    },
    "querylog-engine": {
      text: "The resolver that answered the query. Several values match any of them.",
      topic: "observability",
      anchor: "query-log",
    },
    "querylog-col-reason": {
      text: "What decided the answer: the source, then the list, rule, zone or rewrite that matched. Open a row for every detail.",
      topic: "observability",
      anchor: "query-log",
    },

    // Dashboard
    "dashboard-range": {
      text: "The period the charts and top lists cover. Longer ranges use coarser steps; five-minute summaries are kept for 8 days.",
      default: "Last hour",
      topic: "observability",
      anchor: "dashboard",
    },
    "dashboard-auto-refresh": {
      text: "Reloads the dashboard while it is on: every 10 seconds for ranges up to an hour, every minute for longer ones, and health every 15 seconds.",
      default: "On",
      topic: "observability",
      anchor: "dashboard",
    },
    "dashboard-col-up-on": {
      text: "How many resolvers last reported this upstream as healthy, out of those that use it.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "dashboard-col-rtt": {
      text: "The upstream's measured round-trip time, averaged over the resolvers that report it healthy.",
      topic: "resolution",
      anchor: "upstreams",
    },
    "dashboard-col-filter-index": {
      text: "Memory the resolver's compiled filter index uses for blocklists, categories and policies.",
      topic: "filtering",
      anchor: "blocklists",
    },
    "dashboard-col-config": {
      text: "The configuration version the resolver runs. An arrow shows a newer version it has not applied yet.",
      topic: "fleet",
      anchor: "rollouts",
    },
  },
};
