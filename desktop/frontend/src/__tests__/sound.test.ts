// Run: tsx src/__tests__/sound.test.ts

import { attentionChimeEventKey, shouldPlayAttentionChimeForEvent } from "../lib/sound";

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

console.log("\nsound notifications");

{
  eq(attentionChimeEventKey({ kind: "approval_request", approval: { id: "approval-1" } }), "approval:approval-1", "approval request builds a stable chime key");
  eq(attentionChimeEventKey({ kind: "ask_request", ask: { id: "ask-1" } }), "ask:ask-1", "ask request builds a stable chime key");
  eq(attentionChimeEventKey({ kind: "turn_done" }), undefined, "non-attention events do not build chime keys");
}

{
  const seen = new Set<string>();
  eq(shouldPlayAttentionChimeForEvent({ kind: "approval_request", approval: { id: "approval-1" } }, seen), true, "first approval event plays");
  eq(shouldPlayAttentionChimeForEvent({ kind: "approval_request", approval: { id: "approval-1" } }, seen), false, "replayed approval event is deduped");
  eq(shouldPlayAttentionChimeForEvent({ kind: "ask_request", ask: { id: "ask-1" } }, seen), true, "different ask event still plays");
  eq(shouldPlayAttentionChimeForEvent({ kind: "approval_request" }, seen), false, "malformed approval event does not play");
}

if (failed) {
  console.error(`sound notifications: ${failed} failed, ${passed} passed`);
  process.exit(1);
}

console.log(`sound notifications: ${passed} passed`);
