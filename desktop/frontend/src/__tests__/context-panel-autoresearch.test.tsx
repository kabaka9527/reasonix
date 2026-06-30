// Run: tsx src/__tests__/context-panel-autoresearch.test.tsx

import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { readFileSync } from "fs";
import { AutoResearchSection } from "../components/ContextPanel";
import type { Translator } from "../lib/i18n";
import type { AutoResearchStatusView } from "../lib/types";
import { en } from "../locales/en";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const t: Translator = (key, vars) => {
  let value = en[key] ?? key;
  if (vars) {
    for (const [name, raw] of Object.entries(vars)) {
      value = value.replaceAll(`{${name}}`, String(raw));
    }
  }
  return value;
};

console.log("\ncontext panel autoresearch");

const status: AutoResearchStatusView = {
  taskId: "task-42",
  status: "running",
  iteration: 3,
  staleCount: 2,
  pivotCount: 1,
  pivotRequired: true,
  lastHeartbeatAt: "2026-06-30T00:00:00Z",
  currentDirection: "trace the resume path",
  blocker: "waiting on crash sample",
  openCriteria: [
    {
      id: "criterion-1",
      description: "resume is verified",
      required: true,
      evidenceCount: 0,
      status: "open",
    },
  ],
  taskPath: "/workspace/.reasonix/autoresearch/task-42",
  nextRequiredAction: "pivot to a stronger evidence source",
};

const html = renderToStaticMarkup(
  <AutoResearchSection
    autoResearch={status}
    findings={[
      {
        id: "finding-1",
        kind: "test",
        summary: "resume test passed",
        source: "command",
        command: "go test ./internal/control",
        paths: ["internal/control/controller_test.go"],
        accepted: true,
        createdAt: "2026-06-30T00:00:00Z",
      },
    ]}
    t={t}
    onOpenTask={() => {}}
  />,
);

ok(html.includes("AutoResearch"), "section title renders");
ok(html.includes("task-42"), "task id renders in section metadata");
ok(html.includes("running") && html.includes("3") && html.includes("2"), "status metrics render");
ok(html.includes("trace the resume path"), "current direction renders");
ok(html.includes("resume is verified"), "open success criteria render");
ok(html.includes("2026-06-30T00:00:00Z"), "last heartbeat renders");
ok(html.includes("pivot to a stronger evidence source"), "next required action renders");
ok(html.includes("waiting on crash sample"), "blocker renders");
ok(html.includes("resume test passed") && html.includes("command"), "loaded findings render");
ok(html.includes("button") && html.includes("Open task folder"), "open task folder action renders");

const invalidHtml = renderToStaticMarkup(
  <AutoResearchSection
    autoResearch={{
      ...status,
      status: "invalid",
      blocker: "parse state/progress.json: invalid character",
      openCriteria: [],
    }}
    findings={[]}
    t={t}
    onOpenTask={() => {}}
  />,
);
ok(invalidHtml.includes("invalid"), "invalid status renders");
ok(invalidHtml.includes("parse state/progress.json"), "invalid state error renders");
ok(invalidHtml.includes("Open task folder"), "invalid state still exposes open folder action");

const completeHtml = renderToStaticMarkup(
  <AutoResearchSection
    autoResearch={{
      ...status,
      status: "complete",
      blocker: "",
      openCriteria: [],
    }}
    findings={[]}
    t={t}
  />,
);
ok(completeHtml.includes("complete"), "complete status renders");

const contextPanelSource = readFileSync(new URL("../components/ContextPanel.tsx", import.meta.url), "utf8");
ok(!contextPanelSource.includes("setInterval"), "AutoResearch context refresh does not poll on an interval");
const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
ok(
  appSource.includes('e.kind === "notice"') && appSource.includes('startsWith("autoresearch ")'),
  "AutoResearch lifecycle notices refresh the context panel",
);
const useControllerSource = readFileSync(new URL("../lib/useController.ts", import.meta.url), "utf8");
ok(
  useControllerSource.includes("isAutoResearchNotice") &&
    useControllerSource.includes('startsWith("autoresearch ")') &&
    useControllerSource.includes("refreshMetaForTab(targetTabId, dispatchTo)"),
  "AutoResearch lifecycle notices refresh tab metadata for the status bar",
);

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
