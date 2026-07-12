// Demo fixtures for the fake in-memory transport (see transport.ts).
// One coherent scene: the "acme" monorepo with five projects, a
// three-change stack touching cart -> checkout-api -> storefront, a
// standalone change, an agent-authored change (§8.7 badge), one landed
// and one abandoned change. Everything here is plain data; behavior
// (approve/land/rerun mutations) lives in transport.ts.
import { create } from "@bufbuild/protobuf";
import {
  ActorType,
  ChangeState,
  CommentSide,
  ProjectType,
  Visibility,
  WorkspaceStatus,
  type ChangeSummary,
  type Comment,
  type MergeRequirements,
  type ProjectDetail,
  type WorkspaceSummary,
  type WorkspaceActivityEvent,
  ActorSchema,
  ChangeSummarySchema,
  CommentSchema,
  MergeRequirementsSchema,
  ProjectDetailSchema,
  WorkspaceActivityEventSchema,
  WorkspaceSummarySchema,
} from "../../gen/runko/v1/common_pb";
import {
  WorkspaceEventSchema,
  WorkspaceEventType,
  type WorkspaceEvent,
} from "../../gen/runko/v1/workspaces_pb";
import {
  DiffLineType,
  FileDiffStatus,
  type DiffHunk,
  type FileDiff,
  DiffHunkSchema,
  DiffLineSchema,
  FileDiffSchema,
} from "../../gen/runko/v1/changes_pb";

// Deterministic 40-hex fake SHAs so ids are stable across reloads.
// Absorb the whole seed into two mixed words, then squeeze five blocks -
// a naive per-block hash here once read only the seed's first five chars
// and gave every "head-*" seed the same SHA.
export function fakeSha(seed: string): string {
  let h1 = 0x811c9dc5 >>> 0;
  let h2 = (0x01000193 ^ seed.length) >>> 0;
  for (let i = 0; i < seed.length; i++) {
    h1 = Math.imul(h1 ^ seed.charCodeAt(i), 2654435761) >>> 0;
    h2 = Math.imul(h2 ^ seed.charCodeAt(i), 1597334677) >>> 0;
  }
  let out = "";
  for (let i = 0; out.length < 40; i++) {
    h1 = Math.imul(h1 ^ (h1 >>> 15) ^ h2, 2246822507) >>> 0;
    h2 = Math.imul(h2 ^ (h2 >>> 13) ^ i, 3266489909) >>> 0;
    out += ((h1 ^ h2) >>> 0).toString(16).padStart(8, "0");
  }
  return out.slice(0, 40);
}

const changeId = (seed: string) => `I${fakeSha(seed)}`;

export const TRUNK_SHA = fakeSha("trunk-tip");

// ---------------------------------------------------------------- projects

const project = (
  name: string,
  type: ProjectType,
  owners: string[],
  capabilities: string[],
  declared: string[],
): ProjectDetail =>
  create(ProjectDetailSchema, {
    id: name, // id == name in v1, see common.proto ProjectSummary
    name,
    type,
    path: name,
    visibility: Visibility.DEFAULT,
    effectiveOwners: owners,
    capabilities,
    dependencies: { declared, inferred: [] },
  });

export const projects: ProjectDetail[] = [
  project("commerce/cart", ProjectType.LIBRARY, ["group:commerce"], ["build"], []),
  project(
    "commerce/checkout-api",
    ProjectType.SERVICE,
    ["group:commerce"],
    ["http", "build"],
    ["commerce/cart", "platform/authz"],
  ),
  project("platform/authz", ProjectType.LIBRARY, ["group:platform"], ["rpc"], []),
  project("web/storefront", ProjectType.APP, ["group:web"], [], ["commerce/checkout-api"]),
  project("tools/relbot", ProjectType.JOB, ["group:platform"], [], []),
];

// ----------------------------------------------------------------- changes

const val = { type: ActorType.USER, id: "val" };
const priya = { type: ActorType.USER, id: "priya" };
const refactorBot = { type: ActorType.AGENT, id: "refactor-bot" };

const mkChange = (init: {
  seed: string;
  number: number;
  title: string;
  description?: string;
  baseSha: string;
  headSeed: string;
  author: typeof val;
  state?: ChangeState;
  landedSeed?: string;
  // §12.2 provenance: the workspace branch this Change was pushed from
  // (one branch carries one stack). Omitted = a workspace-less push.
  origin?: { workspace: string; branch: string };
}): ChangeSummary => {
  const id = changeId(init.seed);
  return create(ChangeSummarySchema, {
    id,
    state: init.state ?? ChangeState.OPEN,
    baseSha: init.baseSha,
    headSha: fakeSha(init.headSeed),
    gitRef: `refs/changes/${id}/head`,
    title: init.title,
    description: init.description ?? "",
    landedSha: init.landedSeed ? fakeSha(init.landedSeed) : "",
    // Deterministic per-change land time, in the same fixture epoch the
    // review comments use - only landed changes carry one. Only the first
    // few digits of the id feed the offset: a 40-hex id's full digit run
    // overflows int64.
    landedAt: init.landedSeed
      ? BigInt(1_780_000_000 + Number.parseInt(id.replace(/\D/g, "").slice(0, 5) || "0", 10) * 60)
      : 0n,
    authoredBy: init.author,
    number: BigInt(init.number),
    url: "",
    originWorkspace: init.origin?.workspace ?? "",
    originBranch: init.origin?.branch ?? "",
  });
};

// The stack: cart -> checkout-api -> storefront, each based on the
// previous change's head (the derived relation GetChangeStack serves).
export const stackBottom = mkChange({
  seed: "sku-validate-cart",
  origin: { workspace: "sku-validation", branch: "head" },
  number: 342,
  title: "cart: validate SKU format at parse time",
  description:
    "SKUs were accepted verbatim and only exploded later in checkout.\n" +
    "Parse them into a typed SKU value object up front and reject the\n" +
    "malformed ones with a field-level error.",
  baseSha: TRUNK_SHA,
  headSeed: "head-sku-1",
  author: val,
});

export const stackMiddle = mkChange({
  seed: "sku-reject-checkout",
  origin: { workspace: "sku-validation", branch: "head" },
  number: 343,
  title: "checkout-api: reject invalid SKUs with a structured error",
  description:
    "Builds on the cart-side SKU type: the checkout handler now returns\n" +
    "a structured invalid_sku error (§6.5 shape) instead of a bare 500.",
  baseSha: stackBottom.headSha,
  headSeed: "head-sku-2",
  author: val,
});

export const stackTop = mkChange({
  seed: "sku-surface-storefront",
  origin: { workspace: "sku-validation", branch: "head" },
  number: 344,
  title: "storefront: surface SKU errors inline at add-to-cart",
  description:
    "Renders the checkout API's invalid_sku error next to the quantity\n" +
    "picker instead of the generic toast.",
  baseSha: stackMiddle.headSha,
  headSeed: "head-sku-3",
  author: val,
});

// Forks the stack at stackBottom (same base as stackMiddle): the parallel
// line built on workspace branch "inline-errors" (§12.2 workspace
// branches) - the stacked-diff UI renders this as a real fork, not a
// linearized or duplicated prefix.
export const stackFork = mkChange({
  seed: "sku-inline-cart-errors",
  origin: { workspace: "sku-validation", branch: "inline-errors" },
  number: 346,
  title: "cart: surface SKU errors from the cart API instead",
  description:
    "Parallel approach to #343, built on workspace branch inline-errors:\n" +
    "let the cart API shape the error and skip the checkout hop.",
  baseSha: stackBottom.headSha,
  headSeed: "head-sku-fork",
  author: val,
});

export const soloChange = mkChange({
  seed: "authz-cache",
  origin: { workspace: "authz-cache", branch: "head" },
  number: 341,
  title: "authz: cache group membership lookups (5m TTL)",
  description: "Membership checks were 40% of p99 on hot endpoints.",
  baseSha: TRUNK_SHA,
  headSeed: "head-authz-cache",
  author: priya,
});

export const agentChange = mkChange({
  seed: "bot-config-migrate",
  origin: { workspace: "refactor-bot-cfg", branch: "head" },
  number: 345,
  title: "checkout-api: migrate config parsing to internal/config",
  description:
    "Mechanical migration off the deprecated envconfig helper.\n" +
    "Authored by refactor-bot inside workspace refactor-bot-cfg.",
  baseSha: TRUNK_SHA,
  headSeed: "head-bot-config",
  author: refactorBot,
});

export const landedChange = mkChange({
  seed: "cart-rounding",
  number: 338,
  title: "cart: fix rounding in order totals",
  baseSha: fakeSha("trunk-minus-2"),
  headSeed: "head-rounding",
  author: priya,
  state: ChangeState.LANDED,
  landedSeed: "landed-rounding",
});

export const abandonedChange = mkChange({
  seed: "pricing-dark-launch",
  number: 335,
  title: "pricing: dark-launch experimental engine",
  baseSha: fakeSha("trunk-minus-4"),
  headSeed: "head-pricing",
  author: val,
  state: ChangeState.ABANDONED,
});

export const changes: ChangeSummary[] = [
  stackBottom,
  stackMiddle,
  stackTop,
  stackFork,
  soloChange,
  agentChange,
  landedChange,
  abandonedChange,
];

// baseOnTrunk parity with the live server (2026-07-09): a change is
// rooted on trunk unless its base is another fixture change's head
// (stacked - true of children whether their parent is open or not).
{
  const heads = new Set(changes.map((c) => c.headSha));
  for (const c of changes) c.baseOnTrunk = !heads.has(c.baseSha);
}

// base_behind_trunk parity (2026-07-11): trunk moved under one open
// trunk-rooted change so the demo shows the §13.5 staleness chip.
soloChange.baseBehindTrunk = 2;


// ------------------------------------------------------- merge requirements

const req = (
  changeId: string,
  owners: { required: string[]; satisfied: string[] },
  checks: { required: string[]; passing: string[]; failing?: string[]; pending?: string[] },
): MergeRequirements =>
  create(MergeRequirementsSchema, {
    changeId,
    owners: {
      required: owners.required,
      satisfied: owners.satisfied,
      outstanding: owners.required.filter((o) => !owners.satisfied.includes(o)),
    },
    checks: {
      required: checks.required,
      passing: checks.passing,
      failing: checks.failing ?? [],
      pending: checks.pending ?? [],
      // Every reported (non-pending) check links to its CI run page, the
      // way runko-ci report-check --details-url populates it in prod.
      detailsUrls: Object.fromEntries(
        checks.required
          .filter((n) => !(checks.pending ?? []).includes(n))
          .map((n) => [n, `https://ci.example.com/runs/${encodeURIComponent(n)}`]),
      ),
    },
    // mergeable/blockers are recomputed by the store on load and after
    // every mutation; values here are placeholders.
    mergeable: false,
    blockers: [],
  });

// ------------------------------------------------------- review conversation
// §13.4.1 fixture threads on the stack's middle change: a live line-level
// thread with a reply, and a RESOLVED thread bound to the previous head -
// the outdated rendering (marked, never floated). The agent change carries
// a change-level comment from its bot author (comments allowed, approvals
// never - §8.7).

const mkComment = (init: {
  id: string;
  author: typeof val;
  body: string;
  path?: string;
  line?: number;
  headSha: string;
  parentId?: string;
  resolved?: boolean;
}): Comment =>
  create(CommentSchema, {
    id: init.id,
    author: init.author,
    body: init.body,
    createdAt: BigInt(1_780_000_000 + Number.parseInt(init.id.replace(/\D/g, "") || "0", 10) * 60),
    path: init.path ?? "",
    side: init.line ? CommentSide.HEAD : CommentSide.UNSPECIFIED,
    line: init.line ?? 0,
    headSha: init.headSha,
    parentId: init.parentId ?? "",
    resolved: init.resolved ?? false,
  });

export const comments = new Map<string, Comment[]>([
  [
    stackMiddle.id,
    [
      mkComment({
        id: "cmt-101",
        author: priya,
        body: "Should this also log the raw SKU at debug? invalid_sku responses are going to be the first thing support asks about.",
        path: "commerce/checkout-api/handler.go",
        line: 42,
        headSha: stackMiddle.headSha,
      }),
      mkComment({
        id: "cmt-102",
        author: val,
        body: "Good call — the writeError path already logs the structured code; I'll add the raw value behind the redaction filter.",
        headSha: stackMiddle.headSha,
        parentId: "cmt-101",
      }),
      mkComment({
        id: "cmt-100",
        author: priya,
        body: "Wrap the cart error so the caller can errors.Is on it.",
        path: "commerce/checkout-api/handler.go",
        line: 41,
        headSha: fakeSha("head-sku-2-v1"),
        resolved: true,
      }),
    ],
  ],
  [
    agentChange.id,
    [
      mkComment({
        id: "cmt-110",
        author: refactorBot,
        body: "Mechanical migration, no behavior change intended: 47 call sites moved off envconfig. The two sites with non-default tags are called out in the diff.",
        headSha: agentChange.headSha,
      }),
    ],
  ],
]);

// Pending review requests (§13.4.2): reviewer -> requested_by. priya has
// already commented on the CURRENT head of stackMiddle, so the derived
// attention set puts the ball back in val's (the author's) court.
export const reviewRequests = new Map<string, Map<string, string>>([
  [stackMiddle.id, new Map([["priya", "val"]])],
]);

export const requirements: MergeRequirements[] = [
  req(
    stackBottom.id,
    { required: ["group:commerce"], satisfied: ["group:commerce"] },
    {
      required: ["bazel_test://commerce/cart:cart_test", "manifest-lint", "secrets-scan"],
      passing: ["bazel_test://commerce/cart:cart_test", "manifest-lint", "secrets-scan"],
    },
  ),
  req(
    stackMiddle.id,
    { required: ["group:commerce"], satisfied: [] },
    {
      required: [
        "bazel_test://commerce/checkout-api:handler_test",
        "manifest-lint",
        "secrets-scan",
      ],
      passing: ["manifest-lint", "secrets-scan"],
      pending: ["bazel_test://commerce/checkout-api:handler_test"],
    },
  ),
  req(
    stackTop.id,
    { required: ["group:web"], satisfied: [] },
    {
      required: ["storefront-e2e", "manifest-lint", "secrets-scan"],
      passing: ["manifest-lint", "secrets-scan"],
      failing: ["storefront-e2e"],
    },
  ),
  req(
    stackFork.id,
    { required: ["group:commerce"], satisfied: [] },
    {
      required: ["bazel_test://commerce/cart:cart_test", "manifest-lint"],
      passing: ["bazel_test://commerce/cart:cart_test", "manifest-lint"],
    },
  ),
  req(
    soloChange.id,
    { required: ["group:platform"], satisfied: ["group:platform"] },
    {
      required: ["bazel_test://platform/authz:authz_test", "secrets-scan"],
      passing: ["bazel_test://platform/authz:authz_test", "secrets-scan"],
    },
  ),
  req(
    agentChange.id,
    { required: ["group:commerce"], satisfied: [] },
    {
      required: [
        "bazel_test://commerce/checkout-api:handler_test",
        "bazel_test://commerce/checkout-api:config_test",
        "secrets-scan",
      ],
      passing: ["secrets-scan"],
      pending: [
        "bazel_test://commerce/checkout-api:handler_test",
        "bazel_test://commerce/checkout-api:config_test",
      ],
    },
  ),
  req(
    landedChange.id,
    { required: ["group:commerce"], satisfied: ["group:commerce"] },
    {
      required: ["bazel_test://commerce/cart:cart_test", "secrets-scan"],
      passing: ["bazel_test://commerce/cart:cart_test", "secrets-scan"],
    },
  ),
  req(
    abandonedChange.id,
    { required: ["group:commerce"], satisfied: [] },
    { required: ["secrets-scan"], passing: ["secrets-scan"] },
  ),
];

// -------------------------------------------------------------------- diffs

// Compact hunk builder: lines prefixed "+", "-", or " "; old/new line
// numbers derived from the start positions.
function hunk(oldStart: number, newStart: number, header: string, src: string[]): DiffHunk {
  let oldLine = oldStart;
  let newLine = newStart;
  const lines = src.map((raw) => {
    const marker = raw[0];
    const content = raw.slice(1);
    if (marker === "+") {
      return create(DiffLineSchema, {
        type: DiffLineType.ADDED,
        content,
        oldLine: 0,
        newLine: newLine++,
      });
    }
    if (marker === "-") {
      return create(DiffLineSchema, {
        type: DiffLineType.REMOVED,
        content,
        oldLine: oldLine++,
        newLine: 0,
      });
    }
    return create(DiffLineSchema, {
      type: DiffLineType.CONTEXT,
      content,
      oldLine: oldLine++,
      newLine: newLine++,
    });
  });
  return create(DiffHunkSchema, {
    oldStart,
    oldLines: oldLine - oldStart,
    newStart,
    newLines: newLine - newStart,
    header,
    lines,
  });
}

function file(init: {
  path: string;
  oldPath?: string;
  status: FileDiffStatus;
  project: string;
  binary?: boolean;
  hunks?: DiffHunk[];
}): FileDiff {
  let additions = 0;
  let deletions = 0;
  for (const h of init.hunks ?? []) {
    for (const l of h.lines) {
      if (l.type === DiffLineType.ADDED) additions++;
      if (l.type === DiffLineType.REMOVED) deletions++;
    }
  }
  return create(FileDiffSchema, {
    path: init.path,
    oldPath: init.oldPath ?? "",
    status: init.status,
    hunks: init.hunks ?? [],
    binary: init.binary ?? false,
    additions,
    deletions,
    project: init.project,
  });
}

export const diffs = new Map<string, FileDiff[]>([
  [
    stackBottom.id,
    [
      file({
        path: "commerce/cart/sku.go",
        status: FileDiffStatus.ADDED,
        project: "commerce/cart",
        hunks: [
          hunk(0, 1, "", [
            "+package cart",
            "+",
            '+import "fmt"',
            "+",
            "+// SKU is a validated stock-keeping unit: 3-4 upper-case letters,",
            "+// a dash, then 4-8 digits (e.g. ACME-00421).",
            "+type SKU string",
            "+",
            "+func ParseSKU(raw string) (SKU, error) {",
            "+\tif !skuPattern.MatchString(raw) {",
            '+\t\treturn "", fmt.Errorf("invalid SKU %q: want AAAA-00000 form", raw)',
            "+\t}",
            "+\treturn SKU(raw), nil",
            "+}",
          ]),
        ],
      }),
      file({
        path: "commerce/cart/item.go",
        status: FileDiffStatus.MODIFIED,
        project: "commerce/cart",
        hunks: [
          hunk(12, 12, "func AddItem", [
            " func AddItem(c *Cart, raw string, qty int) error {",
            "-\tif raw == \"\" {",
            '-\t\treturn errors.New("empty SKU")',
            "-\t}",
            "-\tc.items = append(c.items, item{sku: raw, qty: qty})",
            "+\tsku, err := ParseSKU(raw)",
            "+\tif err != nil {",
            "+\t\treturn err",
            "+\t}",
            "+\tc.items = append(c.items, item{sku: sku, qty: qty})",
            " \treturn nil",
            " }",
          ]),
        ],
      }),
      file({
        path: "commerce/cart/sku_test.go",
        status: FileDiffStatus.ADDED,
        project: "commerce/cart",
        hunks: [
          hunk(0, 1, "", [
            "+package cart",
            "+",
            '+import "testing"',
            "+",
            "+func TestParseSKURejectsMalformed(t *testing.T) {",
            '+\tfor _, raw := range []string{"", "acme-1", "ACME_00421", "X-1"} {',
            "+\t\tif _, err := ParseSKU(raw); err == nil {",
            '+\t\t\tt.Errorf("ParseSKU(%q) accepted a malformed SKU", raw)',
            "+\t\t}",
            "+\t}",
            "+}",
          ]),
        ],
      }),
    ],
  ],
  [
    stackMiddle.id,
    [
      file({
        path: "commerce/checkout-api/handler.go",
        status: FileDiffStatus.MODIFIED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(41, 41, "func (s *Server) handleAddItem", [
            " \tif err := cart.AddItem(c, req.SKU, req.Qty); err != nil {",
            "-\t\thttp.Error(w, err.Error(), http.StatusInternalServerError)",
            "+\t\twriteError(w, invalidSKU(req.SKU, err))",
            " \t\treturn",
            " \t}",
          ]),
        ],
      }),
      file({
        path: "commerce/checkout-api/errors.go",
        status: FileDiffStatus.ADDED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(0, 1, "", [
            "+package main",
            "+",
            "+// invalidSKU is the §6.5 structured error shape: code, field,",
            "+// message, suggestion - clients branch on code, never the text.",
            "+func invalidSKU(raw string, err error) apiError {",
            "+\treturn apiError{",
            '+\t\tCode:       "invalid_sku",',
            '+\t\tField:      "items[].sku",',
            "+\t\tMessage:    err.Error(),",
            '+\t\tSuggestion: "SKUs look like ACME-00421; check the catalog export",',
            "+\t}",
            "+}",
          ]),
        ],
      }),
      file({
        path: "commerce/checkout-api/internal/validate/validate.go",
        oldPath: "commerce/checkout-api/internal/validation.go",
        status: FileDiffStatus.RENAMED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(3, 3, "", [
            "-package internal",
            "+package validate",
            " ",
            ' import "github.com/acme/monorepo/commerce/cart"',
          ]),
        ],
      }),
    ],
  ],
  [
    stackTop.id,
    [
      file({
        path: "web/storefront/src/cart/AddToCart.tsx",
        status: FileDiffStatus.MODIFIED,
        project: "web/storefront",
        hunks: [
          hunk(58, 58, "function AddToCart", [
            "   const add = async () => {",
            "     const res = await api.addItem(sku, qty);",
            "-    if (!res.ok) toast.error(\"Something went wrong\");",
            "+    if (!res.ok && res.error.code === \"invalid_sku\") {",
            "+      setSkuError(res.error);",
            "+      return;",
            "+    }",
            "+    if (!res.ok) toast.error(res.error.message);",
            "   };",
          ]),
          hunk(74, 78, "", [
            "     <QuantityPicker value={qty} onChange={setQty} />",
            "+    {skuError && <InlineError error={skuError} />}",
            "     <Button onClick={add}>Add to cart</Button>",
          ]),
        ],
      }),
      file({
        path: "web/storefront/src/cart/InlineError.tsx",
        status: FileDiffStatus.ADDED,
        project: "web/storefront",
        hunks: [
          hunk(0, 1, "", [
            "+export function InlineError({ error }: { error: ApiError }) {",
            "+  return (",
            '+    <p className="inline-error" role="alert">',
            "+      {error.message}",
            "+      {error.suggestion && <span>{error.suggestion}</span>}",
            "+    </p>",
            "+  );",
            "+}",
          ]),
        ],
      }),
      file({
        path: "web/storefront/assets/error-icon.png",
        status: FileDiffStatus.ADDED,
        project: "web/storefront",
        binary: true,
      }),
    ],
  ],
  [
    soloChange.id,
    [
      file({
        path: "platform/authz/cache.go",
        status: FileDiffStatus.ADDED,
        project: "platform/authz",
        hunks: [
          hunk(0, 1, "", [
            "+package authz",
            "+",
            '+import "time"',
            "+",
            "+// membershipCache holds group lookups for five minutes - long",
            "+// enough to flatten the p99, short enough that revocation is",
            "+// still same-shift.",
            "+type membershipCache struct {",
            "+\tttl time.Duration",
            "+}",
          ]),
        ],
      }),
      file({
        path: "platform/authz/authz.go",
        status: FileDiffStatus.MODIFIED,
        project: "platform/authz",
        hunks: [
          hunk(22, 22, "func (a *Authorizer) IsMember", [
            " func (a *Authorizer) IsMember(user, group string) (bool, error) {",
            "+\tif ok, hit := a.cache.get(user, group); hit {",
            "+\t\treturn ok, nil",
            "+\t}",
            " \tok, err := a.directory.IsMember(user, group)",
            "+\tif err == nil {",
            "+\t\ta.cache.put(user, group, ok)",
            "+\t}",
            " \treturn ok, err",
            " }",
          ]),
        ],
      }),
    ],
  ],
  [
    agentChange.id,
    [
      file({
        path: "commerce/checkout-api/config.go",
        status: FileDiffStatus.MODIFIED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(5, 5, "", [
            "-\t\"github.com/acme/monorepo/pkg/envconfig\"",
            "+\t\"github.com/acme/monorepo/internal/config\"",
            " )",
            " ",
            " func loadConfig() (Config, error) {",
            "-\treturn envconfig.Parse[Config]()",
            "+\treturn config.Load[Config]()",
            " }",
          ]),
        ],
      }),
      file({
        path: "commerce/checkout-api/config_test.go",
        status: FileDiffStatus.MODIFIED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(14, 14, "func TestLoadConfigDefaults", [
            "-\tcfg, err := envconfig.Parse[Config]()",
            "+\tcfg, err := config.Load[Config]()",
            " \tif err != nil {",
            " \t\tt.Fatal(err)",
            " \t}",
          ]),
        ],
      }),
    ],
  ],
  [
    landedChange.id,
    [
      file({
        path: "commerce/cart/totals.go",
        status: FileDiffStatus.MODIFIED,
        project: "commerce/cart",
        hunks: [
          hunk(31, 31, "func (c *Cart) Total", [
            "-\treturn float64(cents) / 100",
            "+\treturn money.FromCents(cents)",
          ]),
        ],
      }),
    ],
  ],
  [
    abandonedChange.id,
    [
      file({
        path: "commerce/pricing/engine.go",
        status: FileDiffStatus.ADDED,
        project: "commerce/checkout-api",
        hunks: [
          hunk(0, 1, "", ["+package pricing", "+", "+// experimental, never shipped"]),
        ],
      }),
    ],
  ],
]);

// addedFileDiff builds the all-additions FileDiff for one newly created
// file - what a create-project Change's diff shows (transport.ts).
export function addedFileDiff(fullPath: string, project: string, content: string): FileDiff {
  const lines = content.replace(/\n$/, "").split("\n");
  return file({
    path: fullPath,
    status: FileDiffStatus.ADDED,
    project,
    hunks: [hunk(0, 1, "", lines.map((l) => "+" + l))],
  });
}

// --------------------------------------------------------------- workspaces

const workspace = (init: {
  id: string;
  owner: string;
  affinity: string[];
  writeAllowlist?: string[];
  status?: WorkspaceStatus;
  branches?: string[];
}): WorkspaceSummary =>
  create(WorkspaceSummarySchema, {
    id: init.id,
    owner: init.owner,
    baseRevision: TRUNK_SHA,
    projectAffinity: init.affinity,
    writeAllowlist: init.writeAllowlist ?? [],
    snapshotRef: `refs/workspaces/${init.id}/head`,
    status: init.status ?? WorkspaceStatus.ACTIVE,
    // Parallel lines of work (§12.2 workspace branches) - derived from
    // refs/workspaces/<id>/* on the real daemon.
    branches: init.branches ?? ["head"],
  });

export const workspaces: WorkspaceSummary[] = [
  workspace({
    id: "sku-validation",
    owner: "val",
    affinity: ["commerce/cart", "commerce/checkout-api", "web/storefront"],
    // Two parallel lines in one workspace (§12.2 workspace branches).
    branches: ["head", "inline-errors"],
  }),
  workspace({
    id: "authz-cache",
    owner: "priya",
    affinity: ["platform/authz"],
  }),
  workspace({
    id: "refactor-bot-cfg",
    owner: "refactor-bot",
    affinity: ["commerce/checkout-api"],
    writeAllowlist: ["commerce/checkout-api/"],
  }),
  workspace({
    id: "pricing-spike",
    owner: "val",
    affinity: ["commerce/checkout-api"],
    status: WorkspaceStatus.DETACHED,
  }),
];

// ------------------------------------------- workspace observability (§12.6)

const wsActor = (id: string, agent = false) =>
  create(ActorSchema, { type: agent ? ActorType.AGENT : ActorType.USER, id });

const wsEvent = (init: {
  id: number;
  type: WorkspaceEventType;
  ws: string;
  branch?: string;
  actor?: string;
  agent?: boolean;
  sha?: string;
  changeId?: string;
  files?: number;
  adds?: number;
  dels?: number;
}): WorkspaceEvent =>
  create(WorkspaceEventSchema, {
    id: BigInt(init.id),
    type: init.type,
    workspaceId: init.ws,
    branch: init.branch ?? "head",
    actor: init.actor ? wsActor(init.actor, init.agent) : undefined,
    sha: init.sha ?? "",
    changeId: init.changeId ?? "",
    filesChanged: init.files ?? 0,
    additions: init.adds ?? 0,
    deletions: init.dels ?? 0,
    // Deterministic, id-spaced timestamps (the landedAt fixture lesson).
    occurredAt: BigInt(1_780_000_000 + init.id * 90),
  });

// Stats-only timeline rows (§12.6), per workspace - the real daemon
// records these at receive/land time; newest-first is the SERVE order,
// the fixture stores insertion order like Postgres does.
export const workspaceEvents = new Map<string, WorkspaceEvent[]>([
  [
    "sku-validation",
    [
      wsEvent({ id: 1, type: WorkspaceEventType.SNAPSHOT_PUSHED, ws: "sku-validation", actor: "val", sha: fakeSha("ws-sku-snap-1"), files: 2, adds: 40, dels: 3 }),
      wsEvent({ id: 2, type: WorkspaceEventType.CHANGE_PUSHED, ws: "sku-validation", actor: "val", sha: stackBottom.headSha, changeId: stackBottom.id, files: 2 }),
      wsEvent({ id: 3, type: WorkspaceEventType.SNAPSHOT_PUSHED, ws: "sku-validation", branch: "inline-errors", actor: "val", sha: fakeSha("ws-sku-snap-2"), files: 1, adds: 18 }),
      wsEvent({ id: 4, type: WorkspaceEventType.SNAPSHOT_PUSHED, ws: "sku-validation", actor: "val", sha: fakeSha("ws-sku-snap-3"), files: 3, adds: 66, dels: 12 }),
    ],
  ],
  [
    "refactor-bot-cfg",
    [
      wsEvent({ id: 1, type: WorkspaceEventType.SNAPSHOT_PUSHED, ws: "refactor-bot-cfg", actor: "refactor-bot", agent: true, sha: fakeSha("ws-bot-snap-1"), files: 4, adds: 88, dels: 61 }),
      wsEvent({ id: 2, type: WorkspaceEventType.CHANGE_PUSHED, ws: "refactor-bot-cfg", actor: "refactor-bot", agent: true, sha: agentChange.headSha, changeId: agentChange.id, files: 4 }),
      wsEvent({ id: 3, type: WorkspaceEventType.SNAPSHOT_PUSHED, ws: "refactor-bot-cfg", actor: "refactor-bot", agent: true, sha: fakeSha("ws-bot-snap-2"), files: 1, adds: 9, dels: 2 }),
    ],
  ],
]);

// ---------------------------------------- agent session activity (§12.6.1)

const wsActivityEvent = (init: {
  id: number;
  ws: string;
  kind: string;
  detail: string;
  actor: string;
  agent?: boolean;
  session?: string;
  occurredAt?: number;
}): WorkspaceActivityEvent =>
  create(WorkspaceActivityEventSchema, {
    id: BigInt(init.id),
    workspaceId: init.ws,
    kind: init.kind,
    detail: init.detail,
    actor: wsActor(init.actor, init.agent),
    sessionId: init.session ?? "",
    // Deterministic id-spaced timestamps, except where a row must read as
    // "now" for the presence line (§12.6.1 at-a-glance) to show in /demo.
    occurredAt: BigInt(init.occurredAt ?? 1_780_000_000 + init.id * 45),
  });

// Module-load "now": fresh enough for the presence line, stable within a
// session (the landedAt determinism lesson applies to ORDER, which keys
// on id here, never time).
const activityNow = Math.floor(Date.now() / 1000);

// Harness-reported feeds (§12.6.1): CLIENT-CLAIMED rows the real daemon
// ingests via POST /activity - the bot's feed reads as a live coding
// session, val's as a human who wired the hooks up too.
export const workspaceActivity = new Map<string, WorkspaceActivityEvent[]>([
  [
    "refactor-bot-cfg",
    [
      wsActivityEvent({ id: 1, ws: "refactor-bot-cfg", kind: "read", detail: "commerce/checkout-api/config.go", actor: "refactor-bot", agent: true, session: "sess-refactor-1" }),
      wsActivityEvent({ id: 2, ws: "refactor-bot-cfg", kind: "search", detail: "envconfig.Process", actor: "refactor-bot", agent: true, session: "sess-refactor-1" }),
      wsActivityEvent({ id: 3, ws: "refactor-bot-cfg", kind: "command", detail: "go test ./commerce/checkout-api/...", actor: "refactor-bot", agent: true, session: "sess-refactor-1" }),
      wsActivityEvent({ id: 4, ws: "refactor-bot-cfg", kind: "edit", detail: "commerce/checkout-api/config.go", actor: "refactor-bot", agent: true, session: "sess-refactor-1", occurredAt: activityNow - 15 }),
    ],
  ],
  [
    "sku-validation",
    [
      wsActivityEvent({ id: 1, ws: "sku-validation", kind: "read", detail: "commerce/cart/validate.ts", actor: "val" }),
      wsActivityEvent({ id: 2, ws: "sku-validation", kind: "edit", detail: "commerce/cart/validate.test.ts", actor: "val" }),
    ],
  ],
]);

// Per-branch WIP: the snapshot tip vs base the live view renders. Absent
// entry = no snapshot pushed yet ("" sha), the honest empty state -
// authz-cache stays that way on purpose.
export const workspaceWip = new Map<string, { snapshotSha: string; files: FileDiff[] }>([
  [
    "sku-validation/head",
    {
      snapshotSha: fakeSha("ws-sku-snap-3"),
      files: [
        file({
          path: "commerce/cart/validate.ts",
          status: FileDiffStatus.MODIFIED,
          project: "commerce/cart",
          hunks: [
            hunk(12, 12, "export function validateSku(sku: string) {", [
              " export function validateSku(sku: string) {",
              "-  return sku.length > 0;",
              "+  // WIP: tighten to the catalog's documented SKU grammar.",
              "+  return /^[A-Z]{2,4}-\\d{3,6}$/.test(sku);",
              " }",
            ]),
          ],
        }),
        addedFileDiff(
          "commerce/cart/validate.test.ts",
          "commerce/cart",
          'import { validateSku } from "./validate";\n\ntest("rejects empty", () => {\n  expect(validateSku("")).toBe(false);\n});\n',
        ),
      ],
    },
  ],
  [
    "sku-validation/inline-errors",
    {
      snapshotSha: fakeSha("ws-sku-snap-2"),
      files: [
        addedFileDiff(
          "web/storefront/src/InlineError.tsx",
          "web/storefront",
          "export function InlineError({ message }: { message: string }) {\n  return <p className=\"inline-error\">{message}</p>;\n}\n",
        ),
      ],
    },
  ],
  [
    "refactor-bot-cfg/head",
    {
      snapshotSha: fakeSha("ws-bot-snap-2"),
      files: [
        file({
          path: "commerce/checkout-api/config.go",
          status: FileDiffStatus.MODIFIED,
          project: "commerce/checkout-api",
          hunks: [
            hunk(3, 3, "import (", [
              " import (",
              '-\t"github.com/kelseyhightower/envconfig"',
              '+\t"acme/internal/config"',
              " )",
            ]),
          ],
        }),
      ],
    },
  ],
]);

// --------------------------------------------------------------------- tree

// Trunk-tip filesystem for the repo browser (RepoService). Contents agree
// with the diff/search fixtures where they overlap - one coherent scene.
// Key: repo-root-relative path. Value: file content ("\x00" marks binary).
export const BINARY_MARKER = "\x00binary\x00";

export const fsFiles: Record<string, string> = {
  "README.md": [
    "# acme monorepo",
    "",
    "One repo that feels small. Managed by Runko:",
    "",
    "- `runko project create --name <n> --type <t> --owners group:<g>`",
    "- `runko change push` from any branch - trunk is closed to direct push",
    "- `runko-ci affected` in CI, `runko change land` when gates are green",
    "",
    "Projects live wherever their PROJECT.yaml lives. See OWNERS for the",
    "org default owner.",
  ].join("\n"),
  OWNERS: ["# org default owners (§7.3 fallback)", "group:eng"].join("\n"),
  "commerce/cart/PROJECT.yaml": [
    "name: commerce/cart",
    "type: library",
    "owners:",
    "  - group:commerce",
    "ci:",
    "  checks:",
    "    - bazel_test://commerce/cart:cart_test",
    "    - manifest-lint",
  ].join("\n"),
  "commerce/cart/sku.go": [
    "package cart",
    "",
    'import "fmt"',
    "",
    "// SKU is a validated stock-keeping unit: 3-4 upper-case letters,",
    "// a dash, then 4-8 digits (e.g. ACME-00421).",
    "type SKU string",
    "",
    "func ParseSKU(raw string) (SKU, error) {",
    "\tif !skuPattern.MatchString(raw) {",
    '\t\treturn "", fmt.Errorf("invalid SKU %q: want AAAA-00000 form", raw)',
    "\t}",
    "\treturn SKU(raw), nil",
    "}",
  ].join("\n"),
  "commerce/cart/item.go": [
    "package cart",
    "",
    "func AddItem(c *Cart, raw string, qty int) error {",
    "\tsku, err := ParseSKU(raw)",
    "\tif err != nil {",
    "\t\treturn err",
    "\t}",
    "\tc.items = append(c.items, item{sku: sku, qty: qty})",
    "\treturn nil",
    "}",
  ].join("\n"),
  "commerce/cart/sku_test.go": [
    "package cart",
    "",
    'import "testing"',
    "",
    "func TestParseSKURejectsMalformed(t *testing.T) {",
    '\tfor _, raw := range []string{"", "acme-1", "ACME_00421", "X-1"} {',
    "\t\tif _, err := ParseSKU(raw); err == nil {",
    '\t\t\tt.Errorf("ParseSKU(%q) accepted a malformed SKU", raw)',
    "\t\t}",
    "\t}",
    "}",
  ].join("\n"),
  "commerce/cart/totals.go": [
    "package cart",
    "",
    "func (c *Cart) Total() money.Amount {",
    "\tvar cents int64",
    "\tfor _, it := range c.items {",
    "\t\tcents += it.priceCents * int64(it.qty)",
    "\t}",
    "\treturn money.FromCents(cents)",
    "}",
  ].join("\n"),
  "commerce/checkout-api/PROJECT.yaml": [
    "name: commerce/checkout-api",
    "type: service",
    "owners:",
    "  - group:commerce",
    "dependencies:",
    "  - commerce/cart",
    "  - platform/authz",
    "capabilities:",
    "  - http",
    "  - build",
    "ci:",
    "  checks:",
    "    - bazel_test://commerce/checkout-api:handler_test",
    "    - manifest-lint",
  ].join("\n"),
  "commerce/checkout-api/handler.go": [
    "package main",
    "",
    "func (s *Server) handleAddItem(w http.ResponseWriter, r *http.Request) {",
    "\treq := decodeAddItem(r)",
    "\tif err := cart.AddItem(c, req.SKU, req.Qty); err != nil {",
    "\t\twriteError(w, invalidSKU(req.SKU, err))",
    "\t\treturn",
    "\t}",
    "\twriteJSON(w, http.StatusOK, c)",
    "}",
  ].join("\n"),
  "commerce/checkout-api/errors.go": [
    "package main",
    "",
    "// invalidSKU is the §6.5 structured error shape: code, field,",
    "// message, suggestion - clients branch on code, never the text.",
    "func invalidSKU(raw string, err error) apiError {",
    "\treturn apiError{",
    '\t\tCode:       "invalid_sku",',
    '\t\tField:      "items[].sku",',
    "\t\tMessage:    err.Error(),",
    '\t\tSuggestion: "SKUs look like ACME-00421; check the catalog export",',
    "\t}",
    "}",
  ].join("\n"),
  "commerce/checkout-api/config.go": [
    "package main",
    "",
    'import "github.com/acme/monorepo/internal/config"',
    "",
    "func loadConfig() (Config, error) {",
    "\treturn config.Load[Config]()",
    "}",
  ].join("\n"),
  "commerce/checkout-api/internal/validate/validate.go": [
    "package validate",
    "",
    'import "github.com/acme/monorepo/commerce/cart"',
    "",
    "func Items(items []Item) error {",
    "\tfor _, it := range items {",
    "\t\tif _, err := cart.ParseSKU(it.SKU); err != nil {",
    "\t\t\treturn err",
    "\t\t}",
    "\t}",
    "\treturn nil",
    "}",
  ].join("\n"),
  "platform/authz/PROJECT.yaml": [
    "name: platform/authz",
    "type: library",
    "owners:",
    "  - group:platform",
    "capabilities:",
    "  - rpc",
  ].join("\n"),
  "platform/authz/authz.go": [
    "package authz",
    "",
    "func (a *Authorizer) IsMember(user, group string) (bool, error) {",
    "\tif ok, hit := a.cache.get(user, group); hit {",
    "\t\treturn ok, nil",
    "\t}",
    "\tok, err := a.directory.IsMember(user, group)",
    "\tif err == nil {",
    "\t\ta.cache.put(user, group, ok)",
    "\t}",
    "\treturn ok, err",
    "}",
  ].join("\n"),
  "platform/authz/cache.go": [
    "package authz",
    "",
    'import "time"',
    "",
    "// membershipCache holds group lookups for five minutes - long",
    "// enough to flatten the p99, short enough that revocation is",
    "// still same-shift.",
    "type membershipCache struct {",
    "\tttl time.Duration",
    "}",
  ].join("\n"),
  "web/storefront/PROJECT.yaml": [
    "name: web/storefront",
    "type: app",
    "owners:",
    "  - group:web",
    "dependencies:",
    "  - commerce/checkout-api",
    "ci:",
    "  checks:",
    "    - storefront-e2e",
  ].join("\n"),
  "web/storefront/src/cart/AddToCart.tsx": [
    "export function AddToCart({ sku }: Props) {",
    "  const add = async () => {",
    "    const res = await api.addItem(sku, qty);",
    '    if (!res.ok && res.error.code === "invalid_sku") {',
    "      setSkuError(res.error);",
    "      return;",
    "    }",
    "    if (!res.ok) toast.error(res.error.message);",
    "  };",
    "  return <Button onClick={add}>Add to cart</Button>;",
    "}",
  ].join("\n"),
  "web/storefront/src/cart/InlineError.tsx": [
    "export function InlineError({ error }: { error: ApiError }) {",
    "  return (",
    '    <p className="inline-error" role="alert">',
    "      {error.message}",
    "      {error.suggestion && <span>{error.suggestion}</span>}",
    "    </p>",
    "  );",
    "}",
  ].join("\n"),
  "web/storefront/assets/error-icon.png": BINARY_MARKER,
  "tools/relbot/PROJECT.yaml": [
    "name: tools/relbot",
    "type: job",
    "owners:",
    "  - group:platform",
  ].join("\n"),
  "tools/relbot/main.go": [
    "package main",
    "",
    "// relbot bumps image digests and lands through its bot lane (§14.10.2).",
    "func main() {",
    "\tlane.LandDigestBump()",
    "}",
  ].join("\n"),
};

// ------------------------------------------------------------------- search

export interface SearchDoc {
  path: string;
  project: string;
  lines: string[]; // 1-indexed by position
}

export const searchCorpus: SearchDoc[] = [
  {
    path: "commerce/cart/sku.go",
    project: "commerce/cart",
    lines: [
      "package cart",
      "",
      "// SKU is a validated stock-keeping unit.",
      "type SKU string",
      "",
      "func ParseSKU(raw string) (SKU, error) {",
      "\tif !skuPattern.MatchString(raw) {",
      "\t\treturn \"\", fmt.Errorf(\"invalid SKU %q\", raw)",
      "\t}",
      "\treturn SKU(raw), nil",
      "}",
    ],
  },
  {
    path: "commerce/checkout-api/handler.go",
    project: "commerce/checkout-api",
    lines: [
      "package main",
      "",
      "func (s *Server) handleAddItem(w http.ResponseWriter, r *http.Request) {",
      "\tif err := cart.AddItem(c, req.SKU, req.Qty); err != nil {",
      "\t\twriteError(w, invalidSKU(req.SKU, err))",
      "\t}",
      "}",
    ],
  },
  {
    path: "platform/authz/authz.go",
    project: "platform/authz",
    lines: [
      "package authz",
      "",
      "func (a *Authorizer) IsMember(user, group string) (bool, error) {",
      "\tif ok, hit := a.cache.get(user, group); hit {",
      "\t\treturn ok, nil",
      "\t}",
      "\treturn a.directory.IsMember(user, group)",
      "}",
    ],
  },
  {
    path: "web/storefront/src/cart/AddToCart.tsx",
    project: "web/storefront",
    lines: [
      "export function AddToCart({ sku }: Props) {",
      "  const res = await api.addItem(sku, qty);",
      "  if (!res.ok && res.error.code === \"invalid_sku\") {",
      "    setSkuError(res.error);",
      "  }",
      "}",
    ],
  },
  {
    path: "tools/relbot/main.go",
    project: "tools/relbot",
    lines: [
      "package main",
      "",
      "// relbot bumps image digests and lands through its bot lane (§14.10.2).",
      "func main() {",
      "\tlane.LandDigestBump()",
      "}",
    ],
  },
];

// ------------------------------------------------- history + blame (browse)

/** One fake commit for the browse page's history/blame views. */
export interface FakeCommit {
  sha: string;
  subject: string;
  authorName: string;
  authorEmail: string;
  authoredAt: number; // unix seconds
  changeId: string; // "" = pre-Runko history (no trailer)
  changeState: ChangeState; // UNSPECIFIED when no Change row exists
  paths: string[]; // files this commit touched
}

const daysAgo = (d: number) => Math.floor(Date.now() / 1000) - Math.floor(d * 86400);

/** Newest first, mirroring `git log`. Rows reuse the demo Changes so the
 * history view's change links land on real fixture pages. */
export const fakeHistory: FakeCommit[] = [
  {
    sha: fakeSha("hist-authz-cache"),
    subject: "authz: cache group membership lookups (5m TTL)",
    authorName: "Val Kim",
    authorEmail: "val@acme.dev",
    authoredAt: daysAgo(0.2),
    changeId: soloChange.id,
    changeState: ChangeState.OPEN,
    paths: ["platform/authz/cache.go", "platform/authz/authz.go"],
  },
  {
    sha: fakeSha("landed-rounding"),
    subject: "cart: fix rounding in order totals",
    authorName: "Priya Shah",
    authorEmail: "priya@acme.dev",
    authoredAt: daysAgo(1.5),
    changeId: landedChange.id,
    changeState: ChangeState.LANDED,
    paths: ["commerce/cart/totals.go", "commerce/cart/sku_test.go"],
  },
  {
    sha: fakeSha("hist-inline-error"),
    subject: "storefront: extract InlineError component",
    authorName: "Val Kim",
    authorEmail: "val@acme.dev",
    authoredAt: daysAgo(6),
    changeId: "",
    changeState: ChangeState.UNSPECIFIED,
    paths: ["web/storefront/src/cart/InlineError.tsx", "web/storefront/src/cart/AddToCart.tsx"],
  },
  {
    sha: fakeSha("hist-sku-validation"),
    subject: "cart: validate SKUs at add time",
    authorName: "Priya Shah",
    authorEmail: "priya@acme.dev",
    authoredAt: daysAgo(19),
    changeId: "",
    changeState: ChangeState.UNSPECIFIED,
    paths: ["commerce/cart/sku.go", "commerce/cart/sku_test.go", "commerce/cart/item.go"],
  },
  {
    sha: fakeSha("hist-bootstrap"),
    subject: "monorepo bootstrap: initial projects + OWNERS",
    authorName: "Sam Ortiz",
    authorEmail: "sam@acme.dev",
    authoredAt: daysAgo(120),
    changeId: "",
    changeState: ChangeState.UNSPECIFIED,
    paths: Object.keys(fsFiles),
  },
];

/** Commits touching path ("" = all), newest first. */
export function historyForPath(path: string): FakeCommit[] {
  if (!path) return fakeHistory;
  const clean = path.replace(/\/+$/, "");
  return fakeHistory.filter((c) =>
    c.paths.some((p) => p === clean || p.startsWith(clean + "/")),
  );
}
