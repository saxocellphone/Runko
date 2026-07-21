// "Watch me work": the playground's scripted live run. A timeline of
// beats mutates the SAME in-memory store the fake transport serves
// (api/fake/transport.ts demoScene), so while the show plays the whole
// app is real and clickable - the visitor can open the diff, approve a
// change themselves, or just watch the pages move. Beats are fractions
// of a total runtime, so one script serves the 5-minute default and any
// ?t= override; Director.tsx owns the clock.
//
// The story stays inside the fixture scene (acme's monorepo, val and
// priya, group:commerce): val points a task agent at checkout's flaky
// SKU lookups; the agent works a server-tracked workspace, snapshots,
// pushes a two-change stack, checks run, priya reviews and approves,
// automerge lands bottom-up, and the workspace closes behind it.
import { create } from "@bufbuild/protobuf";
import {
  ActorType,
  ChangeState,
  ChangeSummarySchema,
  CommentSchema,
  MergeRequirementsSchema,
  WorkspaceActivityEventSchema,
  WorkspaceStatus,
  WorkspaceSummarySchema,
} from "../gen/runko/v1/common_pb";
import { WorkspaceEventType, WorkspaceEventSchema } from "../gen/runko/v1/workspaces_pb";
import { addedFileDiff, fakeSha, TRUNK_SHA } from "../api/fake/fixtures";
import {
  recompute,
  recomputeAttention,
  type FakeState,
} from "../api/fake/transport";

export interface Beat {
  // Fraction of the total runtime [0, 1] this beat fires at.
  at: number;
  caption: { title: string; body: string };
  // Videogame-tutorial camera work (Director.tsx): while the visitor is
  // following, the director navigates to `route`, glides the tour cursor
  // onto `selector`, and spotlights it (dim everything else). `pointer:
  // "before"` moves the cursor and pulses BEFORE apply runs - for beats
  // that read as clicking an existing control (approve, rerun) - while
  // the default "after" applies first and then points at what appeared.
  focus?: { route?: string; selector?: string; pointer?: "before" | "after" };
  apply: (state: FakeState) => void;
}

// The cast and artifacts, exported for tests and the Director's links.
export const AGENT = "agent-sku-retry";
export const WS_ID = "checkout-retry";
export const CHANGE_A_ID = "I" + fakeSha("showcase-cart-retry");
export const CHANGE_B_ID = "I" + fakeSha("showcase-checkout-retry");
const SESSION = "sess-showcase";
const agentActor = { type: ActorType.AGENT, id: AGENT };
const priyaActor = { type: ActorType.USER, id: "priya" };

const CHECK_A = "bazel_test://commerce/cart:retry_test";
const CHECK_B = "bazel_test://commerce/checkout-api:handler_test";

const RETRY_GO = `package cart

// Retry wraps the SKU service client with jittered exponential backoff:
// checkout was surfacing raw 503s whenever the catalog redeployed.
func Retry(attempts int, do func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = do(); err == nil {
			return nil
		}
		backoff(i)
	}
	return err
}
`;

const RETRY_TEST_GO = `package cart

func TestRetryStopsOnSuccess(t *testing.T) {
	calls := 0
	err := Retry(5, func() error {
		calls++
		if calls < 3 {
			return errUnavailable
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry: err=%v calls=%d", err, calls)
	}
}
`;

const HANDLER_GO = `// checkout-api: SKU lookups now ride the cart package's retrying
// client instead of a bare HTTP call - a catalog redeploy is a pause,
// not a 500.
client := cart.NewRetryingSkuClient(cfg.CatalogURL)
`;

// Append one §12.6.1 activity row; ids continue the feed's sequence.
function activity(state: FakeState, kind: string, detail: string): void {
  if (!state.workspaces.has(WS_ID)) return;
  const feed = state.workspaceActivity.get(WS_ID) ?? [];
  const nextID = feed.reduce((max, a) => (a.id > max ? a.id : max), 0n) + 1n;
  feed.push(
    create(WorkspaceActivityEventSchema, {
      id: nextID,
      workspaceId: WS_ID,
      kind,
      detail,
      actor: agentActor,
      sessionId: SESSION,
      occurredAt: BigInt(Math.floor(Date.now() / 1000)),
    }),
  );
  state.workspaceActivity.set(WS_ID, feed);
}

// Append one §12.6 timeline event; ids continue the timeline's sequence.
function wsEvent(
  state: FakeState,
  type: WorkspaceEventType,
  init: { sha?: string; changeId?: string; files?: number; adds?: number; dels?: number },
): void {
  if (!state.workspaces.has(WS_ID)) return;
  const list = state.workspaceEvents.get(WS_ID) ?? [];
  const nextID = list.reduce((max, ev) => (ev.id > max ? ev.id : max), 0n) + 1n;
  list.push(
    create(WorkspaceEventSchema, {
      id: nextID,
      type,
      workspaceId: WS_ID,
      branch: "head",
      actor: agentActor,
      sha: init.sha ?? "",
      changeId: init.changeId ?? "",
      filesChanged: init.files ?? 0,
      additions: init.adds ?? 0,
      deletions: init.dels ?? 0,
      occurredAt: BigInt(Math.floor(Date.now() / 1000)),
    }),
  );
  state.workspaceEvents.set(WS_ID, list);
}

// Land one showcase change the way the transport's landChange does, plus
// the workspace timeline row the daemon would record. Skips anything the
// visitor already landed or abandoned - the show never fights the user.
function land(state: FakeState, changeId: string): void {
  const c = state.changes.get(changeId);
  if (!c || c.state !== ChangeState.OPEN) return;
  const r = state.requirements.get(changeId);
  if (r && !r.mergeable) {
    // Automerge only lands green gates; a visitor who un-approved keeps
    // their world consistent and the show just moves on.
    return;
  }
  c.state = ChangeState.LANDED;
  c.landedSha = fakeSha(changeId + "-landed");
  c.landedAt = BigInt(Math.floor(Date.now() / 1000));
  wsEvent(state, WorkspaceEventType.CHANGE_LANDED, { changeId, sha: c.landedSha });
}

function approve(state: FakeState, changeId: string, ownerRef: string): void {
  const r = state.requirements.get(changeId);
  if (!r?.owners) return;
  if (!r.owners.satisfied.includes(ownerRef)) {
    r.owners.satisfied.push(ownerRef);
    r.owners.outstanding = r.owners.outstanding.filter((o) => o !== ownerRef);
  }
  recompute(r);
  recomputeAttention(state, changeId);
}

// Guarded push: beats may replay (the visitor can restart the show), and
// a check name must never appear twice in a requirements bucket.
function pushUnique(arr: string[], v: string): void {
  if (!arr.includes(v)) arr.push(v);
}

function setChecks(
  state: FakeState,
  changeId: string,
  fn: (checks: { passing: string[]; failing: string[]; pending: string[] }) => void,
): void {
  const r = state.requirements.get(changeId);
  if (!r?.checks) return;
  fn(r.checks);
  recompute(r);
}

function comment(
  state: FakeState,
  changeId: string,
  author: typeof agentActor,
  body: string,
  init?: { path?: string; line?: number },
): void {
  const c = state.changes.get(changeId);
  if (!c || c.state !== ChangeState.OPEN) return;
  state.nextCommentID++;
  const list = state.comments.get(changeId) ?? [];
  list.push(
    create(CommentSchema, {
      id: `cmt-${state.nextCommentID}`,
      author,
      body,
      createdAt: BigInt(Math.floor(Date.now() / 1000)),
      path: init?.path ?? "",
      line: init?.line ?? 0,
      headSha: c.headSha,
      resolved: false,
    }),
  );
  state.comments.set(changeId, list);
  recomputeAttention(state, changeId);
}

export const SHOWCASE: Beat[] = [
  {
    at: 0,
    caption: {
      title: "Watch Runko work",
      body:
        "This is acme's monorepo with nothing in flight - no open changes, no workspaces. Everything that appears from here on is built by the run you're watching, live and clickable. The story: checkout surfaces raw 503s whenever the catalog redeploys, and val just pointed a task agent at it.",
    },
    focus: { route: "/changes" },
    // The clean slate: the tutorial builds the world up from zero, so the
    // fixture scene's open stacks and workspaces step aside (the repo's
    // code, projects, and landed history stay - that's the org the agent
    // works IN). Also flags the canned watchWorkspace scene off.
    apply: (state) => {
      state.changes.clear();
      state.requirements.clear();
      state.diffs.clear();
      state.comments.clear();
      state.reviewRequests.clear();
      state.pendingProjects.clear();
      state.workspaces.clear();
      state.workspaceEvents.clear();
      state.workspaceActivity.clear();
      state.workspaceWip.clear();
      state.showcaseActive = true;
    },
  },
  {
    at: 0.03,
    caption: {
      title: "A task identity, not a shared credential",
      body: `val ran \`runko agent create --task sku-retry\` — the agent gets its own name (${AGENT}), its own token, and a TTL. Ten agents means ten identities the server can tell apart.`,
    },
    focus: { route: "/changes" },
    apply: () => {},
  },
  {
    at: 0.06,
    focus: { route: "/workspaces", selector: `a[href$="/workspaces/${WS_ID}"]` },
    caption: {
      title: "One task, one workspace",
      body:
        "The agent created workspace checkout-retry: a server-tracked slice of the monorepo scoped to commerce/checkout-api and commerce/cart. It just appeared in the registry — open it and keep watching from there.",
    },
    apply: (state) => {
      if (state.workspaces.has(WS_ID)) return;
      state.workspaces.set(
        WS_ID,
        create(WorkspaceSummarySchema, {
          id: WS_ID,
          owner: AGENT,
          baseRevision: TRUNK_SHA,
          projectAffinity: ["commerce/checkout-api", "commerce/cart"],
          writeAllowlist: ["commerce/"],
          snapshotRef: `refs/workspaces/${WS_ID}/head`,
          status: WorkspaceStatus.ACTIVE,
          branches: [],
          createdAt: BigInt(Math.floor(Date.now() / 1000)),
        }),
      );
    },
  },
  {
    at: 0.1,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: {
      title: "The agent's session streams to the server",
      body:
        "Every read, search, command, and edit the agent makes is reported to the workspace's activity feed (§12.6.1) — you're watching it think, not waiting for a PR to appear at the end.",
    },
    apply: (state) => activity(state, "read", "commerce/checkout-api/handler.go"),
  },
  {
    at: 0.13,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: { title: "Tracing the failure", body: "Code search across the whole monorepo — no second clone, no guessing which repo the SKU client lives in." },
    apply: (state) => activity(state, "search", "SkuLookup"),
  },
  {
    at: 0.16,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: { title: "Reproducing it first", body: "The agent runs the failing test before touching anything." },
    apply: (state) => activity(state, "command", "bazel test //commerce/checkout-api:handler_test  (2 failed)"),
  },
  {
    at: 0.2,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: {
      title: "Root cause, on the record",
      body: "Notes land in the same feed, so the human who takes over later inherits the reasoning, not just the diff.",
    },
    apply: (state) => activity(state, "note", "root cause: SKU lookups have no backoff — catalog redeploys surface as 503s"),
  },
  {
    at: 0.24,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: { title: "First edit", body: "A retrying SKU client in commerce/cart, where every caller can share it." },
    apply: (state) => activity(state, "edit", "commerce/cart/retry.go"),
  },
  {
    at: 0.28,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="wip-diff"]' },
    caption: {
      title: "Snapshot: the work is on the server now",
      body:
        "runko workspace snapshot pushed the work-in-progress as a durable ref. Kill the agent's machine and nothing is lost — the WIP diff on this page is served from the server, not the agent's disk.",
    },
    apply: (state) => {
      const w = state.workspaces.get(WS_ID);
      if (!w) return;
      if (!w.branches.includes("head")) w.branches.push("head");
      state.workspaceWip.set(`${WS_ID}/head`, {
        snapshotSha: fakeSha("showcase-snap-1"),
        files: [addedFileDiff("commerce/cart/retry.go", "commerce/cart", RETRY_GO)],
      });
      wsEvent(state, WorkspaceEventType.SNAPSHOT_PUSHED, {
        sha: fakeSha("showcase-snap-1"),
        files: 1,
        adds: 14,
      });
    },
  },
  {
    at: 0.33,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: { title: "Tests alongside the fix", body: "The agent keeps working; the feed keeps ticking." },
    apply: (state) => {
      activity(state, "edit", "commerce/cart/retry_test.go");
      activity(state, "edit", "commerce/checkout-api/handler.go");
    },
  },
  {
    at: 0.37,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="agent-activity"]' },
    caption: { title: "Green locally", body: "Both projects' tests pass in the workspace before anything is pushed for review." },
    apply: (state) => activity(state, "command", "bazel test //commerce/... (passed)"),
  },
  {
    at: 0.41,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="wip-diff"]' },
    caption: {
      title: "Second snapshot — the diff grows",
      body: "Snapshots are cheap and frequent. The live WIP view above just picked up the tests and the checkout-side wiring.",
    },
    apply: (state) => {
      const w = state.workspaces.get(WS_ID);
      if (!w) return;
      if (!w.branches.includes("head")) w.branches.push("head");
      state.workspaceWip.set(`${WS_ID}/head`, {
        snapshotSha: fakeSha("showcase-snap-2"),
        files: [
          addedFileDiff("commerce/cart/retry.go", "commerce/cart", RETRY_GO),
          addedFileDiff("commerce/cart/retry_test.go", "commerce/cart", RETRY_TEST_GO),
          addedFileDiff("commerce/checkout-api/handler_retry.go", "commerce/checkout-api", HANDLER_GO),
        ],
      });
      wsEvent(state, WorkspaceEventType.SNAPSHOT_PUSHED, {
        sha: fakeSha("showcase-snap-2"),
        files: 3,
        adds: 39,
      });
    },
  },
  {
    at: 0.46,
    focus: { route: "/changes", selector: `.stack-card:has(a[href$="/changes/${CHANGE_A_ID}"])` },
    caption: {
      title: "Pushed for review — as a stack, not a monolith",
      body:
        "One push opened two changes: the cart retry helper, and checkout-api adopting it. Each is small enough to review, each runs only the checks its own paths require, and the child can't land before its parent.",
    },
    apply: (state) => {
      if (state.changes.has(CHANGE_A_ID)) return;
      const number = Math.max(0, ...[...state.changes.values()].map((c) => Number(c.number)));
      const a = create(ChangeSummarySchema, {
        id: CHANGE_A_ID,
        state: ChangeState.OPEN,
        baseSha: TRUNK_SHA,
        headSha: fakeSha("showcase-head-a"),
        gitRef: `refs/changes/${CHANGE_A_ID}/head`,
        title: "cart: retry SKU lookups with jittered backoff",
        description:
          "WHAT: a Retry helper in commerce/cart wrapping the SKU service\nclient with jittered exponential backoff.\nWHY: catalog redeploys surface as raw 503s in checkout; a redeploy\nshould be a pause, not an error page.",
        authoredBy: agentActor,
        number: BigInt(number + 1),
        originWorkspace: WS_ID,
        originBranch: "head",
        baseOnTrunk: true,
      });
      const b = create(ChangeSummarySchema, {
        id: CHANGE_B_ID,
        state: ChangeState.OPEN,
        baseSha: a.headSha,
        headSha: fakeSha("showcase-head-b"),
        gitRef: `refs/changes/${CHANGE_B_ID}/head`,
        title: "checkout-api: ride the retrying SKU client",
        description:
          "WHAT: checkout's SKU lookups go through cart's new retrying\nclient.\nWHY: stacked on the helper so each step reviews on its own.",
        authoredBy: agentActor,
        number: BigInt(number + 2),
        originWorkspace: WS_ID,
        originBranch: "head",
        baseOnTrunk: false,
      });
      state.changes.set(a.id, a);
      state.changes.set(b.id, b);
      state.diffs.set(a.id, [
        addedFileDiff("commerce/cart/retry.go", "commerce/cart", RETRY_GO),
        addedFileDiff("commerce/cart/retry_test.go", "commerce/cart", RETRY_TEST_GO),
      ]);
      state.diffs.set(b.id, [
        addedFileDiff("commerce/checkout-api/handler_retry.go", "commerce/checkout-api", HANDLER_GO),
      ]);
      for (const [id, check] of [
        [a.id, CHECK_A],
        [b.id, CHECK_B],
      ] as const) {
        const r = create(MergeRequirementsSchema, {
          changeId: id,
          owners: { required: ["group:commerce"], satisfied: [], outstanding: ["group:commerce"] },
          checks: { required: [check, "secrets-scan"], passing: [], failing: [], pending: [check, "secrets-scan"] },
        });
        recompute(r);
        state.requirements.set(id, r);
      }
      wsEvent(state, WorkspaceEventType.CHANGE_PUSHED, { changeId: a.id, sha: a.headSha, files: 2 });
      wsEvent(state, WorkspaceEventType.CHANGE_PUSHED, { changeId: b.id, sha: b.headSha, files: 1 });
    },
  },
  {
    at: 0.52,
    focus: { route: `/changes/${CHANGE_A_ID}`, selector: '[data-tour="merge-gates"]' },
    caption: {
      title: "Checks run scoped to what changed",
      body:
        "Affected computation mapped each change's paths to its projects — the cart change runs cart's tests, not the world's. Secrets scanning rode the push itself.",
    },
    apply: (state) => {
      setChecks(state, CHANGE_A_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== "secrets-scan");
        pushUnique(c.passing, "secrets-scan");
      });
      setChecks(state, CHANGE_B_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== "secrets-scan");
        pushUnique(c.passing, "secrets-scan");
      });
    },
  },
  {
    at: 0.56,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: '[data-tour="merge-gates"]' },
    caption: {
      title: "A check fails — in public",
      body: "checkout-api's handler test flaked. The failure is a first-class fact on the change, not a buried log.",
    },
    apply: (state) => {
      setChecks(state, CHANGE_A_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== CHECK_A);
        pushUnique(c.passing, CHECK_A);
      });
      setChecks(state, CHANGE_B_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== CHECK_B);
        pushUnique(c.failing, CHECK_B);
      });
    },
  },
  {
    at: 0.6,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: '[data-tour="merge-gates"]', pointer: "before" },
    caption: {
      title: "The agent re-runs it",
      body: "One command re-queues the failing check. (It was TestParallelCheckout being TestParallelCheckout.)",
    },
    apply: (state) => {
      activity(state, "command", `runko-ci rerun ${CHECK_B}`);
      setChecks(state, CHANGE_B_ID, (c) => {
        c.failing = c.failing.filter((n) => n !== CHECK_B);
        pushUnique(c.pending, CHECK_B);
      });
    },
  },
  {
    at: 0.64,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: '[data-tour="merge-gates"]' },
    caption: {
      title: "Green — and armed",
      body:
        "All checks pass, and the agent arms automerge on both changes: no babysitting, the server lands each one the moment its gates go green. What's missing is a human.",
    },
    apply: (state) => {
      setChecks(state, CHANGE_B_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== CHECK_B);
        pushUnique(c.passing, CHECK_B);
      });
      for (const id of [CHANGE_A_ID, CHANGE_B_ID]) {
        const c = state.changes.get(id);
        if (c && c.state === ChangeState.OPEN) {
          c.automerge = true;
          c.automergeBy = AGENT;
        }
      }
    },
  },
  {
    at: 0.68,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: '[data-tour="merge-gates"]' },
    caption: {
      title: "Review requested — from a human",
      body:
        "The agent asked priya (group:commerce) for review. Agents can comment but NEVER approve — least of all their own work. That rule lives in the server, not the prompt.",
    },
    apply: (state) => {
      for (const id of [CHANGE_A_ID, CHANGE_B_ID]) {
        if (!state.changes.has(id)) continue;
        const requests = state.reviewRequests.get(id) ?? new Map<string, string>();
        requests.set("priya", AGENT);
        state.reviewRequests.set(id, requests);
        recomputeAttention(state, id);
      }
    },
  },
  {
    at: 0.74,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: ".thread, .conversation-card" },
    caption: {
      title: "priya pushes back",
      body: "A line-level question on the checkout change. The thread binds to the exact commit she reviewed.",
    },
    apply: (state) =>
      comment(state, CHANGE_B_ID, priyaActor, "Does the backoff cap somewhere sane? A catalog outage shouldn't hold checkout requests for 30s.", {
        path: "commerce/checkout-api/handler_retry.go",
        line: 4,
      }),
  },
  {
    at: 0.8,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: ".conversation-card" },
    caption: {
      title: "The agent answers with code",
      body:
        "A reply plus an amended change: the backoff now caps at 2s. The amend re-runs checks — and any approval would have been re-earned, because approvals bind to the reviewed commit.",
    },
    apply: (state) => {
      comment(state, CHANGE_B_ID, agentActor, "Capped at 2s total (3 attempts, jittered). Amended.");
      activity(state, "edit", "commerce/checkout-api/handler_retry.go");
      const b = state.changes.get(CHANGE_B_ID);
      if (b && b.state === ChangeState.OPEN) {
        b.headSha = fakeSha("showcase-head-b-2");
        setChecks(state, CHANGE_B_ID, (c) => {
          c.passing = c.passing.filter((n) => n !== CHECK_B);
          pushUnique(c.pending, CHECK_B);
        });
        wsEvent(state, WorkspaceEventType.CHANGE_PUSHED, { changeId: CHANGE_B_ID, sha: b.headSha, files: 1 });
      }
    },
  },
  {
    at: 0.85,
    focus: { route: `/changes/${CHANGE_B_ID}`, selector: '[data-tour="merge-gates"]' },
    caption: { title: "Checks catch up", body: "The amended change is green again. Every gate is now waiting on exactly one thing: priya." },
    apply: (state) =>
      setChecks(state, CHANGE_B_ID, (c) => {
        c.pending = c.pending.filter((n) => n !== CHECK_B);
        if (!c.passing.includes(CHECK_B)) pushUnique(c.passing, CHECK_B);
      }),
  },
  {
    at: 0.89,
    focus: { route: `/changes/${CHANGE_A_ID}`, selector: '[data-tour="merge-gates"]', pointer: "before" },
    caption: {
      title: "priya approves — a different user, by design",
      body: "group:commerce is satisfied on both changes. The agent authored; a human approved; neither could do the other's part.",
    },
    apply: (state) => {
      approve(state, CHANGE_A_ID, "group:commerce");
      approve(state, CHANGE_B_ID, "group:commerce");
    },
  },
  {
    at: 0.92,
    focus: { route: "/changes", selector: `.stack-card:has(a[href$="/changes/${CHANGE_A_ID}"])` },
    caption: {
      title: "Automerge lands the parent",
      body: "The armed bit fires the moment gates go green: the cart helper rebase-lands onto trunk first — stacks land bottom-up, never out of order.",
    },
    apply: (state) => land(state, CHANGE_A_ID),
  },
  {
    at: 0.95,
    focus: { route: "/changes" },
    caption: { title: "…then the child", body: "checkout-api's change follows its parent onto trunk. Two landings, zero human clicks after the approval." },
    apply: (state) => land(state, CHANGE_B_ID),
  },
  {
    at: 0.97,
    focus: { route: `/workspaces/${WS_ID}`, selector: '[data-tour="ws-header"]' },
    caption: {
      title: "The workspace closes itself",
      body: "Its last open change concluded, so the server closed checkout-retry: one task, one workspace, no stale worktrees piling up.",
    },
    apply: (state) => {
      const open = [...state.changes.values()].some(
        (c) => c.state === ChangeState.OPEN && c.originWorkspace === WS_ID,
      );
      const w = state.workspaces.get(WS_ID);
      if (!w || open) return;
      w.status = WorkspaceStatus.CLOSED;
      wsEvent(state, WorkspaceEventType.WORKSPACE_CLOSED, {});
    },
  },
  {
    at: 1,
    caption: {
      title: "That's the loop",
      body:
        "Agent authored, server guarded, human approved, automerge landed — all of it inspectable while it happened. Replay it, or go click through what it left behind: the landed changes, the closed workspace, the code on trunk.",
    },
    apply: () => {},
  },
];
