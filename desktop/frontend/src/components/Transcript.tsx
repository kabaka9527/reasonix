import { createContext, memo, type CSSProperties, type MouseEvent as ReactMouseEvent, type ReactNode, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Item, LiveStream } from "../lib/useController";
import type { CheckpointMeta } from "../lib/types";
import { useT } from "../lib/i18n";
import { AssistantMessage, TurnActions, UserMessage } from "./Message";
import { ProcessBrainIcon, ProcessCompactIcon, ProcessPhaseIcon } from "./ProcessCard";
import { ToolCard } from "./ToolCard";
import { ArrowDown, ChevronRight } from "lucide-react";
import { Welcome } from "./Welcome";
import { ReadOnlyBatch } from "./ReadOnlyBatch";
import { ToolGroup, isCreationGroupableTool, toolGroupKind, type ToolGroupKind } from "./ToolGroup";
import { getDisplayMode, onDisplayModeChange, type DisplayMode } from "../lib/displayMode";
import { isReadOnlyTool } from "../lib/useController";
import { useGSAPCollapse } from "../lib/useGSAPCollapse";
import { useEntranceAnimation } from "../lib/useEntranceAnimation";
import { useScrollManager } from "../lib/useScrollManager";
import { buildStepGroups, buildTurnGroups, compactQuestionText, createWarmLayerState, lastQuestionTurn, questionAnchorId, questionTurnsById, scrollVersion, warmColdPageForTurn, warmLayerWithColdPageAtLeast, warmLayerWithExpandedTurn, warmLayerWithNextColdPage, warmPagination, warmUserPreview, type QuestionAnchor, type TurnGroup, type WarmLayerState } from "../lib/transcriptGrouping";
import { appendTurnActionCopyText } from "../lib/turnActionCopy";
import { displayReasoningText } from "../lib/reasoningDisplay";

type ToolItem = Extract<Item, { kind: "tool" }>;
type AssistantItem = Extract<Item, { kind: "assistant" }>;
type OpenTurnAction = { turn: number; menu: "summary" | "rewind" };

const QUESTION_NAV_MIN_COUNT = 2;
const LiveStreamContext = createContext<LiveStream | undefined>(undefined);
type AssistantReasoningDisplay = "normal" | "hide";

const LiveAssistantMessage = memo(function LiveAssistantMessage({
  item,
  defaultExpanded = false,
  expandWhileStreaming = true,
  truncateStreamingReasoning = false,
  creationMode = false,
  reasoningDisplay = "normal",
}: {
  item: AssistantItem;
  defaultExpanded?: boolean;
  expandWhileStreaming?: boolean;
  truncateStreamingReasoning?: boolean;
  creationMode?: boolean;
  reasoningDisplay?: AssistantReasoningDisplay;
}) {
  const live = useContext(LiveStreamContext);
  const shown = useMemo(
    () => {
      const merged =
        live && live.id === item.id
          ? {
              ...item,
              text: live.text,
              reasoning: live.reasoning,
              streaming: true,
              reasoningComplete: live.reasoningComplete,
              reasoningDurationMs:
                live.reasoningStartedAt && live.reasoningCompletedAt && live.reasoningCompletedAt >= live.reasoningStartedAt
                  ? live.reasoningCompletedAt - live.reasoningStartedAt
                  : item.reasoningDurationMs,
            }
          : item;
      if (reasoningDisplay === "hide") {
        return { ...merged, reasoning: "", reasoningComplete: true, reasoningDurationMs: undefined };
      }
      return merged;
    },
    [item, live?.id, live?.text, live?.reasoning, live?.reasoningComplete, live?.reasoningStartedAt, live?.reasoningCompletedAt, reasoningDisplay],
  );
  return (
    <AssistantMessage
      item={shown}
      defaultExpanded={defaultExpanded}
      expandWhileStreaming={expandWhileStreaming}
      truncateStreamingReasoning={truncateStreamingReasoning}
      creationMode={creationMode}
    />
  );
});

function InlineAssistantReasoning({ item }: { item: AssistantItem }) {
  const t = useT();
  const live = useContext(LiveStreamContext);
  const shown = live && live.id === item.id
    ? {
        reasoning: live.reasoning,
        streaming: true,
        reasoningComplete: live.reasoningComplete,
      }
    : item;
  const reasoning = shown.reasoning.trim();
  if (!reasoning) return null;
  const visibleReasoning = displayReasoningText(shown.reasoning, {
    streaming: shown.streaming,
    truncateStreaming: true,
  });
  const running = shown.streaming && !shown.reasoningComplete;
  return (
    <div className="turn-collapse__reasoning-phase">
      <div className="turn-collapse__reasoning-head" data-running={running ? "" : undefined}>
        <ProcessBrainIcon size={12} />
        <span>{running ? t("msg.thinkingRunning") : t("msg.thinking")}</span>
        <ChevronRight className="reasoning__chevron reasoning__chevron--open" size={12} />
      </div>
      <div className="turn-collapse__inline-reasoning">{visibleReasoning}</div>
    </div>
  );
}

// ── Layer budgets ─────────────────────────────────────────────────────────────
// Hot zone: the most recent N user turns are always fully rendered. All data
// stays in memory (items[]), so expanding a warm turn is instant — no API call.
// Cold zone: a "load more" button paginates the warm zone in batches.
//
//   items[0]  ─┐
//   ...        │ Cold zone  ───  paginated, shown on "load more"
//              ├────────────  warmTurnStart
//   ...        │ Warm zone  ───  collapsible summary cards (individual expand)
//              ├────────────  hotStartIdx
//   items[N]  ─┤ Hot zone   ───  fully rendered
//   ...        │
//   items[end] ┘

const HOT_TURNS = 30;
const WARM_PAGE_SIZE = 20; // cold-zone pagination batch

// ── Helpers ───────────────────────────────────────────────────────────────────

function processDurationMs(items: Item[]): number {
  return items.reduce((ms, it) => {
    if (it.kind === "tool") return ms + (it.durationMs ?? 0);
    if (it.kind === "assistant") return ms + (it.reasoningDurationMs ?? 0);
    return ms;
  }, 0);
}

function useTick(on: boolean): number {
  const [, setN] = useState(0);
  useEffect(() => {
    if (!on) return;
    const id = window.setInterval(() => setN((n) => n + 1), 1000);
    return () => window.clearInterval(id);
  }, [on]);
  return Date.now();
}

function formatWorkDuration(durationMs: number, t: ReturnType<typeof useT>): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return "";
  const totalSeconds = Math.max(1, Math.round(durationMs / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes <= 0) return t("transcript.durationSeconds", { s: totalSeconds });
  if (seconds <= 0) return t("transcript.durationMinutes", { m: minutes });
  return t("transcript.durationMinutesSeconds", { m: minutes, s: seconds });
}

function workStatusLabel(durationMs: number, running: boolean, t: ReturnType<typeof useT>): string {
  const duration = formatWorkDuration(durationMs, t);
  if (running) {
    return duration ? t("transcript.workingDuration", { duration }) : t("transcript.working");
  }
  return duration ? t("transcript.workedDuration", { duration }) : t("transcript.worked");
}

function assistantReasoningOnly(item: AssistantItem): AssistantItem {
  return { ...item, text: "" };
}

function assistantAnswerOnly(item: AssistantItem): AssistantItem {
  return { ...item, reasoning: "", reasoningComplete: true, reasoningDurationMs: undefined };
}

function assistantHasVisibleAnswer(item: AssistantItem, liveId: string | undefined, liveHasAnswerText: boolean): boolean {
  if (item.text.trim() !== "") return true;
  return liveId === item.id && liveHasAnswerText;
}

// ── Transcript component ──────────────────────────────────────────────────────

export function Transcript({
  items,
  live,
  tabId,
  footerHeight = 0,
  onPrompt,
  onEditPrompt,
  onRewind,
  checkpoints = [],
  actionPending = false,
  rewindDisabled = false,
  running = false,
  questionNavigator = true,
  welcomeVariant = "default",
  creationMode = false,
  actionHoverMenus = false,
  rewindSignal = 0,
  revealSignal = 0,
  hydrating = false,
  hasOlderHistory = false,
  olderHistoryCount = 0,
  loadingOlderHistory = false,
  onLoadOlderHistory,
  turnStartAt,
}: {
  items: Item[];
  live?: LiveStream;
  tabId?: string;
  footerHeight?: number;
  onPrompt: (text: string) => void;
  onEditPrompt?: (turn: number, displayText: string, submitText?: string) => boolean | void | Promise<boolean | void>;
  onRewind?: (turn: number, scope: string) => void;
  checkpoints?: CheckpointMeta[];
  actionPending?: boolean;
  rewindDisabled?: boolean;
  running?: boolean;
  questionNavigator?: boolean;
  welcomeVariant?: "default" | "creation";
  creationMode?: boolean;
  actionHoverMenus?: boolean;
  rewindSignal?: number;
  revealSignal?: number;
  hydrating?: boolean;
  hasOlderHistory?: boolean;
  olderHistoryCount?: number;
  loadingOlderHistory?: boolean;
  onLoadOlderHistory?: () => void;
  turnStartAt?: number;
}) {
  const t = useT();
  const {
    scrollRef,
    stick,
    onScroll,
    onWheelIntent,
    onTouchStartIntent,
    onTouchMoveIntent,
    onKeyScrollIntent,
    isAtBottom,
    smoothScrollTo,
    scrollToBottomAfterLayout,
    trackQuestions,
    scheduleRepinIfWasPinned,
    resizeFrame,
    lastClientHeight,
    lastFooterHeight,
  } = useScrollManager();
  const autoScrollFrame = useRef<number | null>(null);
  const pendingRevealBottomScroll = useRef(false);
  const pendingQuestionJump = useRef<QuestionAnchor | null>(null);
  const sessionKey = useMemo(() => `${items[0]?.id ?? ""}|${items[items.length - 1]?.id ?? ""}`, [items]);
  const warmLayerSessionKey = useMemo(() => `${tabId ?? ""}|${revealSignal}|${items[0]?.id ?? ""}`, [items, revealSignal, tabId]);
  const entranceRef = useEntranceAnimation<HTMLDivElement>(sessionKey, items.length);

  const [displayMode, setDisplayMode] = useState<DisplayMode>(() => getDisplayMode());
  useEffect(() => onDisplayModeChange((mode) => setDisplayMode(mode)), []);

  const cancelStreamingAutoScroll = useCallback(() => {
    if (autoScrollFrame.current !== null) {
      cancelAnimationFrame(autoScrollFrame.current);
      autoScrollFrame.current = null;
    }
  }, []);

  const handleWheelIntent = useCallback((event: React.WheelEvent<HTMLElement>) => {
    if (onWheelIntent(event)) cancelStreamingAutoScroll();
  }, [cancelStreamingAutoScroll, onWheelIntent]);

  const handleTouchMoveIntent = useCallback((event: React.TouchEvent<HTMLElement>) => {
    if (onTouchMoveIntent(event)) cancelStreamingAutoScroll();
  }, [cancelStreamingAutoScroll, onTouchMoveIntent]);

  const handleKeyScrollIntent = useCallback((event: React.KeyboardEvent<HTMLElement>) => {
    if (onKeyScrollIntent(event)) cancelStreamingAutoScroll();
  }, [cancelStreamingAutoScroll, onKeyScrollIntent]);

  const questions = useMemo<QuestionAnchor[]>(() => {
    const anchors: QuestionAnchor[] = [];
    let turn = 0;
    for (const it of items) {
      if (it.kind !== "user") continue;
      anchors.push({ id: it.id, text: compactQuestionText(it.text), turn, checkpointTurn: it.checkpointTurn });
      turn += 1;
    }
    return anchors;
  }, [items]);
  const showQuestionNav = questionNavigator && questions.length >= QUESTION_NAV_MIN_COUNT;

  // Track question count and auto-scroll on new messages.
  useEffect(() => { trackQuestions(questions.length); }, [questions.length, trackQuestions]);

  // Reset the auto-scroll pin when switching tabs so the new session always
  // starts at the bottom. Without this, stick.current from the previous tab
  // persists across React re-renders (Transcript is not keyed by tabId) and
  // disables auto-scroll when the user had scrolled up in the old tab (#4584).
  useEffect(() => {
    stick.current = true;
    pendingRevealBottomScroll.current = true;
  }, [tabId, revealSignal]);

  useEffect(() => {
    if (!pendingRevealBottomScroll.current || items.length === 0) return;
    pendingRevealBottomScroll.current = false;
    const frame = requestAnimationFrame(() => {
      scrollToBottomAfterLayout(5);
    });
    return () => cancelAnimationFrame(frame);
  }, [items.length, revealSignal, scrollToBottomAfterLayout, tabId]);

  // Auto-scroll to bottom during streaming. Coalesce fast token/reasoning
  // updates into one layout read/write per animation frame.
  const contentVersion = useMemo(() => scrollVersion(items), [items]);
  useEffect(() => {
    if (items.length === 0) return;
    if (!stick.current) return;
    if (autoScrollFrame.current !== null) return;
    autoScrollFrame.current = requestAnimationFrame(() => {
      autoScrollFrame.current = null;
      if (!stick.current) return;
      const el = scrollRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    });
  }, [contentVersion, live?.text?.length ?? 0, live?.reasoning?.length ?? 0]);
  useEffect(() => {
    return () => {
      if (autoScrollFrame.current !== null) {
        cancelAnimationFrame(autoScrollFrame.current);
        autoScrollFrame.current = null;
      }
    };
  }, []);

  // ResizeObserver for container height changes.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    lastClientHeight.current = el.clientHeight;
    const observer = new ResizeObserver((entries) => {
      const height = entries[0]?.contentRect.height ?? el.clientHeight;
      const previous = lastClientHeight.current ?? height;
      lastClientHeight.current = height;
      if (items.length === 0) return;
      scheduleRepinIfWasPinned(height - previous);
    });
    observer.observe(el);
    return () => {
      observer.disconnect();
      if (resizeFrame.current !== null) {
        cancelAnimationFrame(resizeFrame.current);
        resizeFrame.current = null;
      }
    };
  }, [items.length, scheduleRepinIfWasPinned]);

  // Footer height changes → smooth scroll repin with GSAP.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const previous = lastFooterHeight.current ?? footerHeight;
    lastFooterHeight.current = footerHeight;
    if (items.length === 0) return;
    scheduleRepinIfWasPinned(previous - footerHeight);
  }, [footerHeight, items.length, scheduleRepinIfWasPinned]);

  // After a non-fork rewind, scroll to the last user message (the
  // rewound-to point) so the user knows where they are.
  useEffect(() => {
    if (rewindSignal <= 0 || questions.length === 0) return;
    const lastQ = questions[questions.length - 1];
    const el = document.getElementById(questionAnchorId(lastQ.id));
    if (!el || !scrollRef.current) return;
    stick.current = false;
    scrollRef.current.scrollTop = el.offsetTop - scrollRef.current.offsetTop - 12;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rewindSignal]);

  // Sub-agent calls carry a parentId; collect them under their parent `task`
  // call so the parent card can render them nested, and skip them at top level.
  const subcallsByParent = useMemo(() => {
    const m = new Map<string, ToolItem[]>();
    for (const it of items) {
      if (it.kind === "tool" && it.parentId) {
        const arr = m.get(it.parentId) ?? [];
        arr.push(it);
        m.set(it.parentId, arr);
      }
    }
    return m;
  }, [items]);

  // ── Layer state ────────────────────────────────────────────────────────────
  const [warmLayerState, setWarmLayerState] = useState<WarmLayerState>(() => createWarmLayerState(warmLayerSessionKey));
  const defaultWarmLayerState = useMemo<WarmLayerState>(() => createWarmLayerState(warmLayerSessionKey), [warmLayerSessionKey]);
  const activeWarmLayerState = warmLayerState.sessionKey === warmLayerSessionKey
    ? warmLayerState
    : defaultWarmLayerState;
  const { expandedWarmTurns, coldPage } = activeWarmLayerState;

  // Compute turn groups from the structural item list. Streaming text updates
  // keep the same items[] reference, so this stays out of the token hot path.
  const turnGroups = useMemo(() => buildTurnGroups(items), [items]);

  // hotStartIdx: first index of the hot zone in items[].
  const hotStartIdx = useMemo(() => {
    let needed = HOT_TURNS;
    for (let i = items.length - 1; i >= 0; i--) {
      if (items[i].kind === "user") {
        needed--;
        if (needed <= 0) return i;
      }
    }
    return 0;
  }, [items]);

  // How many turns are in the cold zone (not yet shown).
  const { warmStartTurn, warmEndTurn, coldTurnCount } = useMemo(
    () => warmPagination({ turnCount: turnGroups.length, hotTurns: HOT_TURNS, pageSize: WARM_PAGE_SIZE, coldPage }),
    [coldPage, turnGroups.length],
  );

  useLayoutEffect(() => {
    const question = pendingQuestionJump.current;
    if (!question) return;
    const node = document.getElementById(questionAnchorId(question.id));
    if (!node) return;
    pendingQuestionJump.current = null;
    stick.current = false;
    smoothScrollTo(node, 12);
  }, [expandedWarmTurns, smoothScrollTo, stick, warmStartTurn]);

  // ── The turn action menu ──────────────────────────────────────────────────
  const [openAction, setOpenAction] = useState<OpenTurnAction | null>(null);
  useEffect(() => {
    if (openAction === null) return;
    const onDown = (e: MouseEvent) => {
      const el = e.target as Element | null;
      if (!el || !el.closest(".turn-actions")) setOpenAction(null);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [openAction]);

  const userTurn = useMemo(() => questionTurnsById(questions), [questions]);
  const lastTurn = useMemo(() => lastQuestionTurn(questions, userTurn), [questions, userTurn]);
  const checkpointsByTurn = useMemo(() => new Map(checkpoints.map((checkpoint) => [checkpoint.turn, checkpoint])), [checkpoints]);

  // ── JumpBar integration ───────────────────────────────────────────────────
  const jumpToQuestion = (question: QuestionAnchor) => {
    const node = document.getElementById(questionAnchorId(question.id));
    if (!node) return;
    pendingQuestionJump.current = null;
    stick.current = false;
    smoothScrollTo(node, 12);
  };

  const handleJumpToQuestion = useCallback((question: QuestionAnchor) => {
    pendingQuestionJump.current = question;
    // Auto-expand the warm turn when jumping to an old question.
    const warmTurnStart = turnGroups.length - HOT_TURNS;
    if (question.turn < warmTurnStart) {
      const neededColdPage = warmColdPageForTurn({
        turn: question.turn,
        turnCount: turnGroups.length,
        hotTurns: HOT_TURNS,
        pageSize: WARM_PAGE_SIZE,
      });
      setWarmLayerState((prev) => {
        const paged = warmLayerWithColdPageAtLeast(prev, warmLayerSessionKey, neededColdPage);
        return warmLayerWithExpandedTurn(paged, warmLayerSessionKey, question.turn, true);
      });
    }
    jumpToQuestion(question);
  }, [turnGroups.length, warmLayerSessionKey]);

  // ── Hot zone: fully rendered from hotStartIdx to end ─────────────────────
  // Memoized separately from the assembly so streaming tokens don't rebuild
  // the warm/cold zone JSX trees. Uses LiveStreamContext for streaming data
  // (added by upstream PR #3423) instead of per-call renderSegments.
  const empty = items.length === 0;

  useLayoutEffect(() => {
    if (!empty) return;
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = 0;
    stick.current = false;
    const frame = requestAnimationFrame(() => {
      el.scrollTop = 0;
    });
    return () => cancelAnimationFrame(frame);
  }, [empty, scrollRef, stick, tabId]);

  // In compact mode, break each turn into step groups.
  // A step = one assistant + its tool results, from one assistant to the next.
  // Each completed non-final step is folded into "Processed".
  const stepGroups = useMemo(() => {
    if (displayMode === "standard") return null;
    return buildStepGroups(items, hotStartIdx);
  }, [displayMode, hotStartIdx, items]);

  // The hot-zone memo must not depend on the live stream's full text/reasoning
  // — that would rebuild the whole element array on every streaming token
  // (LiveAssistantMessage reads those via LiveStreamContext instead). The memo
  // only needs presence flags, which flip at most once per turn.
  const liveId = live?.id;
  const liveHasAnswerText = Boolean(live?.text.trim());
  const liveHasReasoning = Boolean(live?.reasoning);

  const hotZoneNodes = useMemo<ReactNode[]>(() => {
    const out: ReactNode[] = [];
    let actionText = "";
    let actionReady = false;
    let activeTurn: number | undefined;
    const pushTurnActions = () => {
      if (activeTurn == null || !actionReady || actionText.trim() === "") return;
      const turn = activeTurn;
      const openMenu = openAction && openAction.turn === turn ? openAction.menu : null;
      out.push(
        <TurnActions
          key={`ta-${turn}`}
          text={actionText}
          turn={turn}
          openMenu={openMenu}
          onOpenMenu={(menu) => setOpenAction(menu ? { turn, menu } : null)}
          checkpoint={checkpointsByTurn.get(turn)}
          actionPending={actionPending}
          rewindDisabled={rewindDisabled}
          hoverMenus={actionHoverMenus}
          isLastTurn={turn === lastTurn}
          onRewind={(targetTurn, scope) => {
            onRewind?.(targetTurn, scope);
            setOpenAction(null);
          }}
        />,
      );
      actionText = "";
      actionReady = false;
    };

    // Compact mode: step-based rendering
    // Standard mode: flat rendering (no step groups)
    if (stepGroups) {
      // Collect consecutive completed non-final steps into batches
      let processBatch: Item[] = [];
      let processBatchStart: string | null = null;
      const flushProcessBatch = () => {
        if (processBatch.length === 0) return;
        const dur = processDurationMs(processBatch);
        out.push(
          <TurnCollapse
            key={`turn-process-${processBatchStart}`}
            items={processBatch}
            durationMs={dur}
            mode={displayMode}
            subcalls={subcallsByParent}
            tabId={tabId}
            creationMode={creationMode}
            turnStartAt={turnStartAt}
            preferredKind="reasoning"
          />,
        );
        processBatch = [];
        processBatchStart = null;
      };
      // Warnings must stay visible after the fold auto-closes on completion —
      // route them around the process fold; everything else batches into it.
      const pushProcessItems = (list: Item[]) => {
        for (const item of list) {
          if (item.kind === "notice" && item.level === "warn") {
            flushProcessBatch();
            out.push(<NoticeCard key={item.id} level={item.level} text={item.text} detail={item.detail} />);
            continue;
          }
          if (!processBatchStart) processBatchStart = item.id;
          processBatch.push(item);
        }
      };

      for (const group of stepGroups) {
        const first = group.items[0];

        if (first.kind === "user") {
          flushProcessBatch();
          pushTurnActions();
          const tn = userTurn.get(first.id);
          const checkpoint = tn == null ? undefined : checkpointsByTurn.get(tn);
          activeTurn = tn;
          out.push(
            <UserMessage
              key={first.id}
              id={first.id}
              text={first.text}
              submitText={first.submitText}
              failed={first.failed}
              createdAt={first.createdAt}
              turn={tn}
              anchorId={questionAnchorId(first.id)}
              onEdit={onEditPrompt}
              editDisabled={rewindDisabled || !checkpoint?.canConversation}
            />,
          );
          continue;
        }

        // Completed non-final step → batch it
        if (group.isComplete && !group.isFinal) {
          pushProcessItems(group.items);
          continue;
        }

        // Completed final answer → fold every process item for this turn into
        // one reasoning container, then render only the answer text outside it.
        const nonAssistantItems = group.items.filter(
          (it) => it.kind !== "assistant" || (it.streaming && !it.text.trim())
        );
        const hasRunning = nonAssistantItems.some((it) => it.kind === "tool" && it.status === "running");
        const finalAssistants = group.items.filter((it): it is AssistantItem => it.kind === "assistant" && !it.streaming && it.text.trim() !== "");
        if (finalAssistants.length > 0 && !hasRunning) {
          pushProcessItems(nonAssistantItems);
          for (const assistant of finalAssistants) {
            if (!assistant.reasoning) continue;
            if (!processBatchStart) processBatchStart = assistant.id;
            processBatch.push(assistantReasoningOnly(assistant));
          }
          flushProcessBatch();
          for (const assistant of finalAssistants) {
            out.push(
              <LiveAssistantMessage
                key={assistant.id}
                item={assistantAnswerOnly(assistant)}
                defaultExpanded={false}
                expandWhileStreaming={false}
                truncateStreamingReasoning={true}
                creationMode={creationMode}
                reasoningDisplay="hide"
              />,
            );
            actionText = appendTurnActionCopyText(actionText, assistant.text);
            actionReady = true;
          }
          continue;
        }

        // Active step → keep the live process in the same turn-level reasoning
        // fold. The final answer, if it has started streaming, still renders
        // outside the fold with its reasoning hidden.
        pushProcessItems(nonAssistantItems);
        for (const it of group.items) {
          if (it.kind !== "assistant") continue;
          if (!processBatchStart) processBatchStart = it.id;
          processBatch.push(assistantReasoningOnly(it as AssistantItem));
        }
        flushProcessBatch();
        for (const it of group.items) {
          if (it.kind !== "assistant") continue;
          out.push(
            <LiveAssistantMessage
              key={it.id}
              item={it as AssistantItem}
              defaultExpanded={false}
              expandWhileStreaming={false}
              truncateStreamingReasoning={true}
              creationMode={creationMode}
              reasoningDisplay="hide"
            />,
          );
          if (!it.streaming && it.text.trim() !== "") {
            actionText = appendTurnActionCopyText(actionText, it.text);
            actionReady = true;
          }
        }
      }
      flushProcessBatch();
      if (!running) pushTurnActions();
    } else {
      // Standard mode keeps the answer body flat, but process material still
      // belongs to one turn-level reasoning fold.
      let processBatch: Item[] = [];
      let processBatchStart: string | null = null;
      const pushProcessItem = (it: Item) => {
        if (!processBatchStart) processBatchStart = it.id;
        processBatch.push(it);
      };
      const flushProcessBatch = () => {
        if (processBatch.length === 0) return;
        out.push(
          <TurnCollapse
            key={`standard-process-${processBatchStart}`}
            items={processBatch}
            durationMs={processDurationMs(processBatch)}
            mode={displayMode}
            subcalls={subcallsByParent}
            tabId={tabId}
            creationMode={creationMode}
            turnStartAt={turnStartAt}
            preferredKind="reasoning"
          />,
        );
        processBatch = [];
        processBatchStart = null;
      };
      for (let i = hotStartIdx; i < items.length; i++) {
        const it = items[i];
        switch (it.kind) {
          case "user": {
            flushProcessBatch();
            pushTurnActions();
            const tn = userTurn.get(it.id);
            const checkpoint = tn == null ? undefined : checkpointsByTurn.get(tn);
            activeTurn = tn;
            out.push(
              <UserMessage
                key={it.id}
                id={it.id}
                text={it.text}
                submitText={it.submitText}
                failed={it.failed}
                createdAt={it.createdAt}
                turn={tn}
                anchorId={questionAnchorId(it.id)}
                onEdit={onEditPrompt}
                editDisabled={rewindDisabled || !checkpoint?.canConversation}
              />,
            );
            break;
          }
          case "assistant": {
            const assistant = it as AssistantItem;
            if (assistant.reasoning || (liveId === assistant.id && liveHasReasoning)) {
              pushProcessItem(assistantReasoningOnly(assistant));
            }
            if (assistantHasVisibleAnswer(assistant, liveId, liveHasAnswerText)) {
              flushProcessBatch();
              out.push(
                <LiveAssistantMessage
                  key={assistant.id}
                  item={assistantAnswerOnly(assistant)}
                  defaultExpanded={false}
                  creationMode={creationMode}
                  reasoningDisplay="hide"
                />,
              );
            }
            if (!assistant.streaming && assistant.text.trim() !== "") {
              actionText = appendTurnActionCopyText(actionText, assistant.text);
              actionReady = true;
            }
            break;
          }
          case "tool":
            pushProcessItem(it);
            break;
          case "phase":
            pushProcessItem(it);
            break;
          case "notice":
            // Warnings must stay visible after the fold auto-closes on
            // completion — only info-level notices belong to the process fold.
            if (it.level === "warn") {
              flushProcessBatch();
              out.push(<NoticeCard key={it.id} level={it.level} text={it.text} detail={it.detail} />);
            } else {
              pushProcessItem(it);
            }
            break;
          case "compaction":
            pushProcessItem(it);
            break;
        }
      }
      flushProcessBatch();
      if (!running) pushTurnActions();
    }
    return out;
  }, [hotStartIdx, items, openAction, actionPending, rewindDisabled, running, onEditPrompt, onRewind, subcallsByParent, userTurn, checkpointsByTurn, displayMode, stepGroups, tabId, actionHoverMenus, creationMode, lastTurn, turnStartAt, liveId, liveHasAnswerText, liveHasReasoning]);

  // ── Assemble rendered output ──────────────────────────────────────────────
  // Warm/cold zone is a separate memo'd WarmZone component so streaming tokens
  // don't rebuild it. The hot zone uses LiveAssistantMessage (reads live from
  // LiveStreamContext) so streaming updates are captured immediately.
  return (
    <div className="transcript-shell">
      <div
        className={`transcript${empty ? " transcript--empty" : ""}`}
        ref={scrollRef}
        onScroll={onScroll}
        onWheelCapture={handleWheelIntent}
        onTouchStartCapture={onTouchStartIntent}
        onTouchMoveCapture={handleTouchMoveIntent}
        onKeyDownCapture={handleKeyScrollIntent}
      >
        {empty && !hydrating && <Welcome onPrompt={onPrompt} variant={welcomeVariant} />}

        <LiveStreamContext.Provider value={live}>
          {hasOlderHistory && (
            <button
              type="button"
              className="warm-collapse"
              onClick={onLoadOlderHistory}
              disabled={loadingOlderHistory}
            >
              {loadingOlderHistory ? t("common.loading") : t("transcript.showEarlierHistory", { n: olderHistoryCount })}
            </button>
          )}
          {turnGroups.length > HOT_TURNS && (
            <WarmZone
              turnGroups={turnGroups}
              expandedWarmTurns={expandedWarmTurns}
              warmStartTurn={warmStartTurn}
              warmEndTurn={warmEndTurn}
              coldTurnCount={coldTurnCount}
              scrollRef={scrollRef}
              warmItems={items}
              warmSubcalls={subcallsByParent}
              warmUserTurn={userTurn}
              warmCheckpoints={checkpointsByTurn}
              warmLastTurn={lastTurn}
              warmDisplayMode={displayMode}
              warmOpenAction={openAction}
              warmActionPending={actionPending}
              warmRewindDisabled={rewindDisabled}
              warmActionHoverMenus={actionHoverMenus}
              warmOnRewind={onRewind}
              warmSetOpenAction={setOpenAction}
              warmOnEdit={onEditPrompt}
              tabId={tabId}
              creationMode={creationMode}
              onToggleColdPage={() => setWarmLayerState((prev) => warmLayerWithNextColdPage(prev, warmLayerSessionKey))}
              onToggleWarmTurn={(g, expand) => {
                setWarmLayerState((prev) => warmLayerWithExpandedTurn(prev, warmLayerSessionKey, g, expand));
              }}
            />
          )}
          <div ref={entranceRef}>
            {hotZoneNodes}
          </div>
        </LiveStreamContext.Provider>
      </div>

      {!empty && showQuestionNav && (
        <QuestionJumpBar questions={questions} onJump={handleJumpToQuestion} />
      )}

      {!empty && !isAtBottom && (
        <button
          type="button"
          className="transcript__jump-bottom"
          onClick={() => scrollToBottomAfterLayout(2)}
          aria-label={t("transcript.jumpToBottom")}
          title={t("transcript.jumpToBottom")}
        >
          <ArrowDown size={18} strokeWidth={2.2} aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

// ── WarmZone sub-component (React.memo for streaming isolation) ────────────
// Receives structural props only; reads streaming state (items, live) via refs
// so it never invalidates on streaming token arrival.

const WarmZone = memo(function WarmZone({
  turnGroups,
  expandedWarmTurns,
  warmStartTurn,
  warmEndTurn,
  coldTurnCount,
  scrollRef,
  warmItems,
  warmSubcalls,
  warmUserTurn,
  warmCheckpoints,
  warmLastTurn,
  warmDisplayMode,
  warmOpenAction,
  warmActionPending,
  warmRewindDisabled,
  warmActionHoverMenus,
  warmOnRewind,
  warmSetOpenAction,
  warmOnEdit,
  tabId,
  creationMode,
  onToggleColdPage,
  onToggleWarmTurn,
}: {
  turnGroups: TurnGroup[];
  expandedWarmTurns: ReadonlySet<number>;
  warmStartTurn: number;
  warmEndTurn: number;
  coldTurnCount: number;
  scrollRef: React.RefObject<HTMLDivElement | null>;
  warmItems: readonly Item[];
  warmSubcalls: ReadonlyMap<string, ToolItem[]>;
  warmUserTurn: ReadonlyMap<string, number>;
  warmCheckpoints: ReadonlyMap<number, CheckpointMeta>;
  warmLastTurn?: number;
  warmDisplayMode: DisplayMode;
  warmOpenAction: OpenTurnAction | null;
  warmActionPending: boolean;
  warmRewindDisabled: boolean;
  warmActionHoverMenus: boolean;
  warmOnRewind: ((turn: number, scope: string) => void) | undefined;
  warmSetOpenAction: (action: OpenTurnAction | null) => void;
  warmOnEdit?: (turn: number, displayText: string, submitText?: string) => boolean | void | Promise<boolean | void>;
  tabId?: string;
  creationMode?: boolean;
  onToggleColdPage: () => void;
  onToggleWarmTurn: (g: number, expand: boolean) => void;
}) {
  const t = useT();
  const out: React.ReactNode[] = [];

  // 1. Cold zone: paginated warm turns (show more button).
  if (coldTurnCount > 0) {
    out.push(
      <button
        key="cold-load-more"
        type="button"
        className="warm-collapse"
        onClick={onToggleColdPage}
      >
        {t("transcript.showEarlierHistory", { n: coldTurnCount })}
      </button>,
    );
  }

  // 2. Warm zone: collapsed/expanded warm turn cards.
  if (turnGroups.length > HOT_TURNS) {
    for (let g = warmStartTurn; g < warmEndTurn; g++) {
      const group = turnGroups[g];
      if (!group) continue;
      const expanded = expandedWarmTurns.has(g);

      if (expanded) {
        const userText = group.userItem.kind === "user" ? group.userItem.text : "";
        out.push(
          <WarmTurnCard
            key={`warm-${g}`}
            userText={warmUserPreview(userText)}
            assistantPreview={group.assistantPreview}
            toolCount={group.toolCount}
            expanded={true}
            onToggle={() => onToggleWarmTurn(g, false)}
          >
            {/* Expanded warm turns render items that are stable (never the
                streaming turn), so this captures items/live via a ref. */}
            <WarmTurnItems
              startIdx={group.startIdx}
              endIdx={group.endIdx}
              items={warmItems}
              subcalls={warmSubcalls}
              userTurnMap={warmUserTurn}
              checkpoints={warmCheckpoints}
              openAction={warmOpenAction}
              actionPending={warmActionPending}
              rewindDisabled={warmRewindDisabled}
              actionHoverMenus={warmActionHoverMenus}
              onRewind={warmOnRewind}
              setOpenAction={warmSetOpenAction}
              onEdit={warmOnEdit}
              tabId={tabId}
              creationMode={creationMode}
              lastTurn={warmLastTurn}
              mode={warmDisplayMode}
            />
          </WarmTurnCard>,
        );
      } else {
        const userText = group.userItem.kind === "user" ? group.userItem.text : "";
        out.push(
          <WarmTurnCard
            key={`warm-${g}`}
            userText={warmUserPreview(userText)}
            assistantPreview={group.assistantPreview}
            toolCount={group.toolCount}
            expanded={false}
            onToggle={() => {
              onToggleWarmTurn(g, true);
              const el = scrollRef.current;
              const node = document.getElementById(questionAnchorId(group.userItem.id));
              if (el && node) {
                requestAnimationFrame(() => {
                  el.scrollTo({ top: node.offsetTop - el.offsetTop - 80, behavior: "smooth" });
                });
              }
            }}
          />,
        );
      }
    }
  }

  return out;
});

function WarmTurnItems({
  startIdx,
  endIdx,
  items,
  subcalls,
  userTurnMap,
  checkpoints,
  openAction,
  actionPending,
  rewindDisabled,
  actionHoverMenus,
  onRewind,
  setOpenAction,
  onEdit,
  tabId,
  creationMode = false,
  lastTurn,
  mode,
}: {
  startIdx: number;
  endIdx: number;
  items: readonly Item[];
  subcalls: ReadonlyMap<string, ToolItem[]>;
  userTurnMap: ReadonlyMap<string, number>;
  checkpoints: ReadonlyMap<number, CheckpointMeta>;
  openAction: OpenTurnAction | null;
  actionPending: boolean;
  rewindDisabled: boolean;
  actionHoverMenus: boolean;
  onRewind: ((turn: number, scope: string) => void) | undefined;
  setOpenAction: (action: OpenTurnAction | null) => void;
  onEdit?: (turn: number, displayText: string, submitText?: string) => boolean | void | Promise<boolean | void>;
  tabId?: string;
  creationMode?: boolean;
  lastTurn?: number;
  mode: DisplayMode;
}) {
  const nodes: React.ReactNode[] = [];
  let actionText = "";
  let actionReady = false;
  let activeTurn: number | undefined;
  const pushTurnActions = () => {
    if (activeTurn == null || !actionReady || actionText.trim() === "") return;
    const turn = activeTurn;
    const openMenu = openAction && openAction.turn === turn ? openAction.menu : null;
    nodes.push(
      <TurnActions
        key={`ta-${turn}`}
        text={actionText}
        turn={turn}
        openMenu={openMenu}
        onOpenMenu={(menu) => setOpenAction(menu ? { turn, menu } : null)}
        checkpoint={checkpoints.get(turn)}
        actionPending={actionPending}
        rewindDisabled={rewindDisabled}
        hoverMenus={actionHoverMenus}
        isLastTurn={turn === lastTurn}
        onRewind={(targetTurn, scope) => {
          onRewind?.(targetTurn, scope);
          setOpenAction(null);
        }}
      />,
    );
    actionText = "";
    actionReady = false;
  };

  // Warm turns render the same turn-level process fold as the hot zone —
  // scrolling back must not flip a turn from folded to flat. Warm items never
  // stream, so there is no live merging here; RO/creation tool grouping happens
  // inside TurnCollapse.
  let processBatch: Item[] = [];
  let processBatchStart: string | null = null;
  const flushProcessBatch = () => {
    if (processBatch.length === 0) return;
    nodes.push(
      <TurnCollapse
        key={`warm-process-${processBatchStart}`}
        items={processBatch}
        durationMs={processDurationMs(processBatch)}
        mode={mode}
        subcalls={subcalls}
        tabId={tabId}
        creationMode={creationMode}
        preferredKind="reasoning"
      />,
    );
    processBatch = [];
    processBatchStart = null;
  };
  const pushProcessItem = (it: Item) => {
    if (!processBatchStart) processBatchStart = it.id;
    processBatch.push(it);
  };

  for (let i = startIdx; i < endIdx && i < items.length; i++) {
    const it = items[i];
    switch (it.kind) {
      case "user": {
        flushProcessBatch();
        pushTurnActions();
        const tn = userTurnMap.get(it.id);
        const checkpoint = tn == null ? undefined : checkpoints.get(tn);
        activeTurn = tn;
        nodes.push(
          <UserMessage
            key={it.id}
            text={it.text}
            submitText={it.submitText}
            failed={it.failed}
            createdAt={it.createdAt}
            turn={tn}
            anchorId={questionAnchorId(it.id)}
            onEdit={onEdit}
            editDisabled={rewindDisabled || !checkpoint?.canConversation}
          />,
        );
        break;
      }
      case "assistant": {
        if (it.reasoning) pushProcessItem(assistantReasoningOnly(it));
        if (it.text.trim() !== "") {
          flushProcessBatch();
          nodes.push(<AssistantMessage key={it.id} item={assistantAnswerOnly(it)} defaultExpanded={false} creationMode={creationMode} />);
        }
        if (!it.streaming && it.text.trim() !== "") {
          actionText = appendTurnActionCopyText(actionText, it.text);
          actionReady = true;
        }
        break;
      }
      case "tool":
        pushProcessItem(it);
        break;
      case "phase":
        pushProcessItem(it);
        break;
      case "notice":
        // Same contract as the hot zone: warnings stay outside the fold so
        // they remain visible once the fold is collapsed.
        if (it.level === "warn") {
          flushProcessBatch();
          nodes.push(<NoticeCard key={it.id} level={it.level} text={it.text} detail={it.detail} />);
        } else {
          pushProcessItem(it);
        }
        break;
      case "compaction":
        pushProcessItem(it);
        break;
    }
  }
  flushProcessBatch();
  pushTurnActions();
  return nodes;
}

// ── Warm turn summary card ────────────────────────────────────────────────────

function WarmTurnCard({
  userText,
  assistantPreview,
  toolCount,
  expanded,
  onToggle,
  children,
}: {
  userText: string;
  assistantPreview: string;
  toolCount: number;
  expanded: boolean;
  onToggle: () => void;
  children?: React.ReactNode;
}) {
  const t = useT();
  const contentRef = useRef<HTMLDivElement>(null);
  const prevHeightRef = useRef(0);
  useGSAPCollapse(contentRef, expanded, { prevHeight: prevHeightRef.current });
  // Always render both children so the container's scrollHeight reflects
  // the correct content at all times.  The inactive one is display:none.
  return (
    <div className={`warm-turn${expanded ? " warm-turn--expanded" : ""}`}>
      <button
        type="button"
        className="warm-turn__head"
        onClick={() => {
          // Capture height before DOM swap so the collapse animation
          // starts from the correct (expanded) height.
          const el = contentRef.current;
          if (el) {
            el.style.height = "auto";
            prevHeightRef.current = el.scrollHeight;
          }
          onToggle();
        }}
        aria-expanded={expanded}
      >
        <span className="warm-turn__chevron">
          <ChevronRight className={expanded ? "warm-turn__chevron--open" : ""} size={13} />
        </span>
        <span className="warm-turn__preview">{userText}</span>
        <span className="warm-turn__meta">
          {toolCount > 0 && <span>{t("transcript.toolCount", { n: toolCount })}</span>}
        </span>
      </button>
      <div ref={contentRef} className="warm-turn__content">
        <div className="warm-turn__body" style={{ display: expanded ? undefined : "none" }}>{children}</div>
        {assistantPreview && (
          <div className="warm-turn__assistant" style={{ display: expanded ? "none" : undefined }}>{assistantPreview}</div>
        )}
      </div>
    </div>
  );
}

// ── TurnCollapse: compact mode grouping ──────────────────────────────────────

type TurnCollapseProps = {
  items: Item[];       // intermediate items (tools, reasoning, phase)
  durationMs: number;  // summed tool execution time across the batch; 0 when unknown
  mode: DisplayMode;
  subcalls: ReadonlyMap<string, ToolItem[]>;
  tabId?: string;
  creationMode?: boolean;
  turnStartAt?: number;
  preferredKind?: "tool" | "reasoning" | "process";
};

function TurnCollapse({ items, durationMs, mode, subcalls, tabId, creationMode = false, turnStartAt, preferredKind }: TurnCollapseProps) {
  const t = useT();
  const live = useContext(LiveStreamContext);
  const [open, setOpen] = useState(false);
  const userOverriddenOpen = useRef(false);
  const prevRunningRef = useRef(false);
  const bodyRef = useRef<HTMLDivElement>(null);

  // Keep only items the body will actually render — an expandable fold over
  // nothing is worse than no fold.
  const displayItems = useMemo(() => {
    return items.filter((it) => {
      if (it.kind === "assistant") {
        if (it.text.trim() !== "") return true;
        return Boolean(it.reasoning || (live?.id === it.id && live.reasoning));
      }
      if (it.kind === "phase") return true;
      if (it.kind === "notice") return true;
      if (it.kind === "compaction") return true;
      if (it.kind !== "tool") return false;
      if (it.parentId || it.name === "todo_write" || it.name === "exit_plan_mode") return false;
      return true;
    });
  }, [items, mode, live?.id, live?.reasoning]);

  const seconds = Math.round(durationMs / 1000);

  const hasRunningProcess = displayItems.some((it) => {
    if (it.kind === "tool") return it.status === "running";
    if (it.kind !== "assistant") return false;
    if (live?.id === it.id) return !live.reasoningComplete;
    return it.streaming && !it.reasoningComplete;
  });
  const hasLiveAssistant = displayItems.some((it) => it.kind === "assistant" && live?.id === it.id);
  const hasRunningWork = hasRunningProcess || hasLiveAssistant;
  const now = useTick(hasRunningWork);
  const runningDurationMs = hasRunningWork
    ? turnStartAt
      ? Math.max(0, now - turnStartAt)
      : live?.reasoningStartedAt
        ? Math.max(0, now - live.reasoningStartedAt)
        : 0
    : 0;
  const effectiveDurationMs = hasRunningWork ? Math.max(durationMs, runningDurationMs) : durationMs;

  useGSAPCollapse(bodyRef, open);
  useEffect(() => {
    const wasRunning = prevRunningRef.current;
    prevRunningRef.current = hasRunningWork;
    if (hasRunningWork) {
      if (!wasRunning) userOverriddenOpen.current = false;
      if (!userOverriddenOpen.current) setOpen(true);
    } else if (wasRunning && !userOverriddenOpen.current) {
      setOpen(false);
    }
  }, [hasRunningWork]);

  if (displayItems.length === 0) return null;

  const collapseKind = preferredKind ?? (displayItems.some((it) => it.kind === "tool")
    ? "tool"
    : displayItems.some((it) => it.kind === "assistant" && Boolean(it.reasoning))
      ? "reasoning"
      : "process");
  const label = collapseKind === "reasoning"
    ? workStatusLabel(effectiveDurationMs, hasRunningWork, t)
    : seconds > 0
      ? t("transcript.processedDuration", { s: seconds })
      : t("transcript.processed");
  const creationLabel = collapseKind === "tool"
    ? t("creation.toolCallsLabel")
    : collapseKind === "reasoning"
      ? label
      : label;

  // Pre-compute body: group consecutive completed read-only tools into ReadOnlyBatch
  const body: ReactNode[] = [];
  const roBatch: ToolItem[] = [];
  const toolBatch: ToolItem[] = [];
  let toolBatchKind: ToolGroupKind | null = null;
  const flushRO = () => {
    if (roBatch.length === 0) return;
    body.push(<ReadOnlyBatch key={`rob-${roBatch[0].id}`} items={[...roBatch]} subcalls={subcalls} tabId={tabId} />);
    roBatch.length = 0;
  };
  const flushToolBatch = () => {
    if (!toolBatchKind || toolBatch.length === 0) return;
    body.push(<ToolGroup key={`tg-${toolBatch[0].id}`} kind={toolBatchKind} items={[...toolBatch]} subcalls={subcalls} tabId={tabId} />);
    toolBatch.length = 0;
    toolBatchKind = null;
  };
  for (const it of displayItems) {
    if (creationMode && it.kind === "tool" && isCreationGroupableTool(it as ToolItem)) {
      const kind = toolGroupKind(it as ToolItem);
      if (kind) {
        if (toolBatchKind && toolBatchKind !== kind) flushToolBatch();
        toolBatchKind = kind;
        toolBatch.push(it as ToolItem);
        continue;
      }
    }
    if (it.kind !== "tool") {
      flushToolBatch();
      flushRO();
    }
    if (!creationMode && it.kind === "tool" && !it.parentId && it.name !== "todo_write" && it.name !== "exit_plan_mode" && it.status !== "running" && isReadOnlyTool(it.name)) {
      roBatch.push(it as ToolItem);
      continue;
    }
    if (it.kind === "tool") {
      flushToolBatch();
      flushRO();
    }
    switch (it.kind) {
      case "tool":
        if (it.parentId) break;
        if (it.name === "todo_write") break;
        if (it.name === "exit_plan_mode") break;
        body.push(<ToolCard key={it.id} item={it as ToolItem} subcalls={subcalls.get(it.id)} tabId={tabId} />);
        break;
      case "phase": body.push(<PhaseCard key={it.id} text={it.text} />); break;
      case "notice": body.push(<NoticeCard key={it.id} level={it.level} text={it.text} detail={it.detail} />); break;
      case "compaction": body.push(<CompactionCard key={it.id} item={it} />); break;
      case "assistant": {
        body.push(<InlineAssistantReasoning key={it.id} item={it as AssistantItem} />);
        break;
      }
    }
  }
  flushToolBatch();
  flushRO();

  return (
    <div className={`turn-collapse${open ? " turn-collapse--open" : ""}`} data-kind={collapseKind} data-entrance={displayItems[0]?.id || undefined}>
      <button
        type="button"
        className="reasoning__head"
        onClick={() => {
          userOverriddenOpen.current = true;
          setOpen((v) => !v);
        }}
        aria-expanded={open}
      >
        <span className="turn-collapse__label" data-creation-label={creationLabel}>{label}</span>
        {!hasRunningWork && <ChevronRight className={`reasoning__chevron${open ? " reasoning__chevron--open" : ""}`} size={12} />}
      </button>
      <div ref={bodyRef} className="turn-collapse__body">{body}</div>
    </div>
  );
}

// ── JumpBar, PhaseCard, NoticeCard, CompactionCard ────────────────────────────

function QuestionJumpBar({ questions, onJump }: { questions: QuestionAnchor[]; onJump: (question: QuestionAnchor) => void }) {
  const t = useT();
  const [hovered, setHovered] = useState<number | null>(null);
  const [active, setActive] = useState<number | null>(null);
  const barRef = useRef<HTMLDivElement>(null);
  const previewTop = useRef(0);
  const [showPreview, setShowPreview] = useState(false);

  useEffect(() => {
    if (questions.length === 0) return;
    setActive(questions[questions.length - 1]?.turn ?? null);
  }, [questions]);

  useEffect(() => {
    if (active === null) return;
    const el = barRef.current?.querySelector(`[data-turn="${active}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const hoverIdx = hovered !== null ? questions.findIndex((question) => question.turn === hovered) : -1;
  const hoveredQuestion = hovered !== null ? questions.find((question) => question.turn === hovered) : undefined;

  const closestQuestionFromY = (clientY: number): { question: QuestionAnchor; previewY: number } | null => {
    const el = barRef.current;
    if (!el) return null;
    const markers = el.querySelectorAll<HTMLElement>(".jump-item");
    const barRect = el.getBoundingClientRect();
    let closest = -1;
    let closestDist = Infinity;
    let closestY = 0;
    markers.forEach((item, index) => {
      const rect = item.getBoundingClientRect();
      const midY = rect.top + rect.height / 2;
      const dist = Math.abs(clientY - midY);
      if (dist < closestDist) {
        closestDist = dist;
        closest = index;
        closestY = midY - barRect.top;
      }
    });
    const question = questions[closest];
    if (!question) return null;
    return { question, previewY: closestY };
  };

  const onMove = (e: ReactMouseEvent<HTMLDivElement>) => {
    const closest = closestQuestionFromY(e.clientY);
    if (!closest) return;
    previewTop.current = closest.previewY;
    setHovered(closest.question.turn);
    setShowPreview(true);
  };

  const scrollTo = (question: QuestionAnchor) => {
    setActive(question.turn);
    onJump(question);
  };

  const onRailMouseDown = (e: ReactMouseEvent<HTMLDivElement>) => {
    const closest = closestQuestionFromY(e.clientY);
    if (!closest) return;
    e.preventDefault();
    previewTop.current = closest.previewY;
    setHovered(closest.question.turn);
    setShowPreview(true);
    scrollTo(closest.question);
  };

  const onItemMouseDown = (e: ReactMouseEvent<HTMLButtonElement>, question: QuestionAnchor) => {
    e.preventDefault();
    scrollTo(question);
  };

  const dotProps = (
    idx: number,
    turn: number,
  ): { style: CSSProperties; "data-d"?: string } => {
    const isActive = active === turn;
    if (hoverIdx < 0) {
      return { style: { width: isActive ? 18 : 12, background: isActive ? "var(--accent)" : undefined } };
    }
    const d = Math.abs(idx - hoverIdx);
    const width = d === 0 ? 32 : d === 1 ? 20 : d === 2 ? 14 : isActive ? 18 : 12;
    const background = d <= 2 ? undefined : isActive ? "var(--accent)" : undefined;
    return {
      style: { width, transitionDelay: `${d * 20}ms`, background },
      "data-d": d <= 2 ? String(d) : undefined,
    };
  };

  return (
    <nav
      className="jump-bar"
      ref={barRef}
      aria-label={t("questionNav.label")}
      onMouseMove={onMove}
      onMouseLeave={() => {
        setHovered(null);
        setShowPreview(false);
      }}
    >
      <div className="jump-scroll" onMouseDown={onRailMouseDown} onClick={onRailMouseDown}>
        {questions.map((question, index) => (
          <button
            className="jump-item"
            key={question.id}
            type="button"
            data-turn={question.turn}
            aria-label={t("questionNav.jump", { n: question.turn + 1 })}
            onMouseDown={(e) => onItemMouseDown(e, question)}
            onClick={(e) => {
              e.stopPropagation();
              if (e.detail === 0) scrollTo(question);
            }}
          >
            <span className="jump-dot" {...dotProps(index, question.turn)} />
          </button>
        ))}
      </div>
      {showPreview && hoveredQuestion && (
        <div className="jump-preview" style={{ top: previewTop.current }} role="tooltip">
          <span className="jump-text">{hoveredQuestion.text}</span>
        </div>
      )}
    </nav>
  );
}

type CompactionItem = Extract<Item, { kind: "compaction" }>;
type NoticeItem = Extract<Item, { kind: "notice" }>;

function PhaseCard({ text }: { text: string }) {
  return <div className="phase" data-entrance="true"><ProcessPhaseIcon size={12} /><span>{text}</span></div>;
}

function NoticeCard({ level, text, detail }: { level: NoticeItem["level"]; text: string; detail?: string }) {
  const t = useT();
  return (
    <div className={`notice-line notice-line--${level}`} data-entrance="true">
      <span className="notice-line__icon">{level === "warn" ? "⚠ " : "ℹ "}</span>
      <div className="notice-line__text">
        {text}
        {detail ? (
          <details className="notice-line__details">
            <summary>{t("notice.details")}</summary>
            <div>{detail}</div>
          </details>
        ) : null}
      </div>
    </div>
  );
}

function CompactionCard({ item }: { item: CompactionItem }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  if (item.pending) {
    return <div className="compaction compaction--pending" data-entrance={item.id}><ProcessCompactIcon size={12} /><span>{t("compaction.working")}</span></div>;
  }
  return (
    <div className="compaction" data-entrance={item.id}>
      <button type="button" className="compaction__head" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <ProcessCompactIcon size={12} />
        <span>{t("compaction.title")}</span>
        <span className="compaction__meta">{t("compaction.messages", { n: item.messages })}{item.trigger ? ` · ${item.trigger}` : ""}</span>
        <ChevronRight className={open ? "compaction__chevron--open" : ""} size={12} />
      </button>
      {open && <pre className="compaction__body">{item.summary}</pre>}
    </div>
  );
}
