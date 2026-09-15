import type { HelpArea } from "./types";

// Tasks 23-30 add their AI pages and components to `pages` as they create them.
export const aiHelp: HelpArea = {
  pages: [
    "pages/ai/AiStatusPage.tsx",
    "pages/ai/AiInsightsPage.tsx",
    "pages/ai/AiRecommendationsPage.tsx",
    "pages/ai/AiAssistantPage.tsx",
    "pages/ai/AiForecastsPage.tsx",
    "components/ai/AiOff.tsx",
    "components/ai/QueryLogAsk.tsx",
    "components/ai/AiTaskStatus.tsx",
    "components/ai/JsonDiff.tsx",
    "components/ai/FindingCard.tsx",
    "components/ai/ProposalCard.tsx",
    "components/ai/ProposalApplyDialog.tsx",
  ],
  entries: {
    "ai-findings-kind": {
      text: "Which findings to show: anomalies the query-log agent detected, or insights the dashboard agent correlated across the fleet.",
      topic: "ai",
      anchor: "insights",
    },
    "ai-findings-status": {
      text: "Filter findings by state. Open findings need attention; acknowledged ones stay listed until resolved; dismissed ones are hidden from the dashboard.",
      default: "open",
      topic: "ai",
      anchor: "insights",
    },
    "querylog-ai-ask": {
      text: "Ask about the query log in plain language. The model turns the question into query-log filters and summarises the matching records; nothing is changed.",
      range: "1 to 500 characters",
      topic: "ai",
    },
    "ai-proposals-status": {
      text: "Filter proposals by state: open, applied, failed, stale (the target changed before apply), dismissed or superseded by a newer proposal.",
      default: "open",
      topic: "ai",
      anchor: "recommendations",
    },
    "ai-apply-acknowledge-license": {
      text: "Required when an action enables a category whose source list is licensed for non-commercial use only. Without it the apply of that action fails.",
      default: "off",
      effect: "Sent as acknowledge_license with the apply request",
      topic: "ai",
      anchor: "recommendations",
    },
    "ai-proposal-select": {
      text: "Select proposals to apply or dismiss together.",
      range: "Up to 100 proposals",
      topic: "ai",
      anchor: "recommendations",
    },
    "ai-dismiss-reason": {
      text: "Why the suggestion does not fit, kept with the proposal for other operators. A dismissed change is not suggested again for 7 days.",
      range: "Up to 500 characters",
      topic: "ai",
      anchor: "recommendations",
    },
    "ai-assistant-message": {
      text: "Describe the configuration change you want. The assistant answers and may attach a proposal, which you review and apply like any other.",
      range: "1 to 2,000 characters",
      topic: "ai",
      anchor: "assistant",
    },
    "ai-forecasts-kind": {
      text: "Upstream predictions project each upstream's response-time trend; capacity forecasts project cache, memory, list and query-volume growth.",
      topic: "ai",
      anchor: "forecasts",
    },
    "ai-threat-domains": {
      text: "Domain names to classify, one per line. Each is checked against the query log and the model's threat assessment; nothing is blocked automatically.",
      range: "1 to 100 names",
      topic: "ai",
      anchor: "threat-checks",
    },
    "ai-rpz-select-all": {
      text: "Select every suggested RPZ rule on this page.",
      topic: "ai",
      anchor: "rpz-suggestions",
    },
    "ai-rpz-select": {
      text: "Select this suggested rule. Applying adds the selected rules to the ai-suggested.rpz zone.",
      topic: "ai",
      anchor: "rpz-suggestions",
    },
  },
};
