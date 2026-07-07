// Run: tsx src/__tests__/composer-run-strip.test.tsx
//
// The run state lives inside the composer card (no floating pill, no layout
// jump), stop has a fixed home next to send, and a pending approval/ask shifts
// the strip into a waiting state instead of a ticking "working" spinner.

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { Composer } from "../components/Composer";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";
import type { CollaborationMode, ToolApprovalMode, TokenMode } from "../lib/types";

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

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function flushTimers(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.HTMLTextAreaElement = dom.window.HTMLTextAreaElement;
  globalThis.Event = dom.window.Event;
  globalThis.CustomEvent = dom.window.CustomEvent;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.InputEvent = dom.window.InputEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.PointerEvent = dom.window.MouseEvent as unknown as typeof PointerEvent;
  globalThis.MutationObserver = dom.window.MutationObserver;
  globalThis.File = dom.window.File;
  globalThis.FileReader = dom.window.FileReader;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  globalThis.ResizeObserver = TestResizeObserver;
  Object.defineProperty(dom.window.HTMLElement.prototype, "attachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, "detachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: () => ({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }),
  });
  return dom;
}

async function renderComposer(props: Partial<Parameters<typeof Composer>[0]> = {}) {
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  const calls = { cancel: 0 };
  let currentProps: Parameters<typeof Composer>[0] = {
    running: false,
    collaborationMode: "normal" as CollaborationMode,
    toolApprovalMode: "ask" as ToolApprovalMode,
    tokenMode: "full" as TokenMode,
    goal: "",
    cwd: "/repo",
    modelLabel: "DeepSeek-R1",
    onSend: () => {},
    onCancel: () => {
      calls.cancel += 1;
      return undefined;
    },
    onCycleMode: () => {},
    onSetMode: () => {},
    onSetCollaborationMode: () => {},
    onSetToolApprovalMode: () => {},
    onToggleYoloApprovalMode: () => {},
    onClearGoal: () => {},
    onSwitchModel: () => {},
    onSetEffort: () => {},
    onSetTokenMode: () => {},
    ready: true,
    ...props,
  };
  const paint = async (nextProps: Partial<Parameters<typeof Composer>[0]> = {}) => {
    currentProps = { ...currentProps, ...nextProps };
    await act(async () => {
      root.render(
        <LocaleProvider>
          <ToastProvider>
            <Composer {...currentProps} />
          </ToastProvider>
        </LocaleProvider>,
      );
      await flushTimers();
    });
  };
  await paint();
  return { root, calls, rerender: paint };
}

console.log("\ncomposer run strip");

// Idle: no strip, no stop button, plain send arrow.
{
  const dom = installDom();
  const { root } = await renderComposer();

  eq(document.querySelector(".composer-run-strip"), null, "idle composer renders no run strip");
  eq(document.querySelector(".composer__btn--stop"), null, "idle composer renders no stop button");
  ok(document.querySelector(".composer__btn--send") !== null, "idle composer keeps the send button");
  eq(document.querySelector(".composer-toolbar--status-only"), null, "floating status pill is gone");

  await act(async () => {
    root.unmount();
  });
  dom.window.close();
}

// Running: strip lives inside the card, ticker is aria-hidden, stop cancels.
{
  const dom = installDom();
  const { root, calls } = await renderComposer({ running: true, turnStartAt: Date.now() });

  const strip = document.querySelector(".composer-card .composer-run-strip");
  ok(strip !== null, "running strip renders inside the composer card");
  const ticker = strip?.querySelector(".composer-run-strip__text");
  eq(ticker?.getAttribute("aria-hidden"), "true", "ticking spinner text stays out of the accessibility tree");
  const live = strip?.querySelector(".sr-only[role=\"status\"]");
  eq(live?.textContent, "Reasonix is working", "live region announces the stable state text only");
  ok(document.querySelector(".composer-card--running") !== null, "running card keeps its running modifier");

  const stop = document.querySelector(".composer__btn--stop") as HTMLButtonElement | null;
  if (!stop) throw new Error("running composer stop button did not render");
  await act(async () => {
    stop.click();
    await flushTimers();
  });
  eq(calls.cancel, 1, "stop button next to send cancels the turn");

  await act(async () => {
    root.unmount();
  });
  dom.window.close();
}

// Waiting on approval: strip switches to the waiting state and stops ticking.
{
  const dom = installDom();
  const { root, rerender } = await renderComposer({ running: true, turnStartAt: Date.now() });

  await rerender({ pendingApprovalLabel: "Run command" });

  const strip = document.querySelector(".composer-run-strip");
  ok(strip?.classList.contains("composer-run-strip--waiting") === true, "pending approval shifts the strip into waiting");
  const text = strip?.querySelector(".composer-run-strip__text");
  eq(text?.textContent, "Waiting for your approval — Run command", "waiting strip names the tool awaiting approval");
  eq(text?.getAttribute("aria-hidden"), null, "waiting text is static and stays accessible");
  eq(document.querySelector(".composer-card--running"), null, "waiting card hands the running accent off to the prompt card");
  ok(document.querySelector(".composer-card--waiting") !== null, "waiting card takes the waiting modifier");

  await rerender({ pendingApprovalLabel: null, pendingAsk: true });
  eq(
    document.querySelector(".composer-run-strip__text")?.textContent,
    "Waiting for your answer",
    "pending ask question shows the ask waiting state",
  );

  await rerender({ pendingAsk: false });
  ok(
    document.querySelector(".composer-run-strip__text")?.getAttribute("aria-hidden") === "true",
    "resolving the prompt returns the strip to the ticking spinner",
  );

  await act(async () => {
    root.unmount();
  });
  dom.window.close();
}

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
