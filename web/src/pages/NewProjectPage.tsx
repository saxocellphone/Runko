import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { projectsClient } from "../api/client";
import type { PreviewCreateProjectResponse } from "../gen/runko/v1/projects_pb";
import { useDebounced } from "../lib/useDebounced";
import { BackLink, EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

const TYPES = ["service", "library", "app", "job", "other"] as const;

// Built-in template languages (§10.4, Bazel-maturity set); "" is the Go
// default and is sent as-is so web-created default projects stay
// manifest-identical to CLI-created ones (language echoed verbatim,
// never default-filled). "Other…" is the no_template escape hatch.
const LANGUAGES = [
  ["", "Go (default)"],
  ["python", "Python"],
  ["ts", "TypeScript"],
  ["rust", "Rust"],
  ["java", "Java"],
  ["cpp", "C++"],
  ["__other__", "Other…"],
] as const;

// Build scaffold (§14.5.5); "" defers to the language default (ts → Vite,
// everything else → Bazel). Vite is a territory scaffold, not a build-graph
// engine — a vite project carries no `build` capability binding.
const BUILD_ENGINES = [
  ["", "Default for language"],
  ["bazel", "Bazel (BUILD.bazel + build binding)"],
  ["vite", "Vite (package.json + vite.config.ts)"],
  ["none", "None (no build scaffold)"],
] as const;

// The API surface, decided at creation (§13.3.1). A service must answer —
// "None" is a valid answer, silence is not — so there is deliberately no
// preselection: the choice is the point.
const API_SURFACES = [
  {
    value: "grpc",
    title: "gRPC",
    detail: "A proto contract lives inside the project; clients declare a consumes edge.",
  },
  {
    value: "rest",
    title: "REST",
    detail: "openapi.yaml, scaffolded — and mandatory for as long as the surface exists.",
  },
  {
    value: "none",
    title: "None",
    detail: "No API. Others use this code through build dependencies only.",
  },
] as const;

// The §10.1 create flow, kept deliberately small (anti-Boq, §2.3): name +
// type (+ the API answer for services) is the whole request, owners
// optional, everything else generated — the preview pane shows exactly the
// files that will be committed. Create opens an ordinary Change (trunk is
// closed, §6.9); landing it through the normal gates makes the project real.
export function NewProjectPage() {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [type, setType] = useState<string>("service");
  const [api, setApi] = useState<string>("");
  const [langChoice, setLangChoice] = useState<string>("");
  const [buildEngine, setBuildEngine] = useState<string>("");
  const [otherLang, setOtherLang] = useState("");
  const [ownersText, setOwnersText] = useState("");
  const [busy, setBusy] = useState(false);
  const [createError, setCreateError] = useState<ConnectError | undefined>();

  const owners = ownersText.split(/[\s,]+/).filter(Boolean);
  const usingOther = langChoice === "__other__";
  const language = usingOther ? otherLang.trim() : langChoice;
  // Only serving types get an API surface (§13.3.1, refined 2026-07-15):
  // the picker exists for services (required) and apps (optional); a
  // library/job/other never sees the choice and always sends none.
  const apiEligible = type === "service" || type === "app";
  // A service must answer the API question before anything is sent —
  // previewing without it would just relay api_required back (§13.3.1).
  const apiMissing = type === "service" && api === "";
  const intent = {
    name: name.trim(),
    type,
    api: apiEligible ? api : "",
    owners,
    language,
    noTemplate: usingOther,
    buildEngine,
  };
  const debouncedKey = useDebounced(JSON.stringify({ ...intent, apiMissing }), 350);

  const [preview, setPreview] = useState<PreviewCreateProjectResponse | undefined>();
  const [previewError, setPreviewError] = useState<ConnectError | undefined>();
  const [previewing, setPreviewing] = useState(false);

  useEffect(() => {
    const parsed = JSON.parse(debouncedKey) as typeof intent & { apiMissing: boolean };
    if (!parsed.name || parsed.apiMissing) {
      setPreview(undefined);
      setPreviewError(undefined);
      return;
    }
    let stale = false;
    setPreviewing(true);
    projectsClient
      .previewCreateProject({ intent: parsed })
      .then((res) => {
        if (stale) return;
        setPreview(res);
        setPreviewError(undefined);
      })
      .catch((err: unknown) => {
        if (stale) return;
        setPreview(undefined);
        setPreviewError(ConnectError.from(err));
      })
      .finally(() => {
        if (!stale) setPreviewing(false);
      });
    return () => {
      stale = true;
    };
  }, [debouncedKey]);

  const createProject = async () => {
    setBusy(true);
    setCreateError(undefined);
    try {
      const res = await projectsClient.createProject({ intent });
      navigate(`/changes/${res.change!.id}`);
    } catch (err) {
      setCreateError(ConnectError.from(err));
      setBusy(false);
    }
  };

  return (
    <div className="page">
      <BackLink to="/projects">Projects</BackLink>
      <header className="page-header">
        <h1 className="page-title">New project</h1>
        <p className="page-sub">
          Name, type, and the API answer are the whole request — everything else is generated
          <InfoTip text="Creating a project opens an ordinary change carrying the generated files (trunk only moves by landing changes). Land it and the project exists; abandon it and nothing ever happened." />
        </p>
      </header>

      <div className="change-layout">
        <div>
          <section className="card new-project-form">
            <div className="form-group">
              <div className="form-eyebrow"><span>Identity</span></div>

              <div className="form-field">
                <label htmlFor="np-name">Name</label>
                <input
                  id="np-name"
                  type="text"
                  placeholder="payments-api"
                  value={name}
                  autoFocus
                  onChange={(e) => setName(e.target.value)}
                />
                <span className="form-hint">
                  Lowercase letters, digits, and hyphens; the repo path defaults to the name.
                </span>
              </div>

              <div className="form-field">
                <label id="np-type-label">Type</label>
                <div className="seg" role="radiogroup" aria-labelledby="np-type-label">
                  {TYPES.map((t) => (
                    <button
                      key={t}
                      type="button"
                      role="radio"
                      aria-checked={type === t}
                      className={"seg-item" + (type === t ? " active" : "")}
                      onClick={() => {
                        setType(t);
                        // A picked surface must not silently ride a switch
                        // to a type that never offered the choice.
                        if (t !== "service" && t !== "app") setApi("");
                      }}
                    >
                      {t}
                    </button>
                  ))}
                </div>
              </div>

              {apiEligible && (
              <div className="form-field">
                <label id="np-api-label">
                  API surface{" "}
                  {type === "service" ? (
                    <span className="form-required">required for services</span>
                  ) : (
                    <span className="form-optional">optional</span>
                  )}
                </label>
                <div className="api-cards" role="radiogroup" aria-labelledby="np-api-label">
                  {API_SURFACES.map((s) => (
                    <label key={s.value} className={"api-card" + (api === s.value ? " active" : "")}>
                      <input
                        type="radio"
                        name="np-api"
                        value={s.value}
                        checked={api === s.value}
                        onChange={() => setApi(s.value)}
                      />
                      <span className="api-card-title">{s.title}</span>
                      <span className="api-card-detail">{s.detail}</span>
                    </label>
                  ))}
                </div>
                <span className="form-hint">
                  Decided at creation: how other projects talk to this one. “None” is a real
                  answer — a service just has to give it.
                </span>
              </div>
              )}
            </div>

            <div className="form-group">
              <div className="form-eyebrow">
                <span>
                  Scaffold <span className="form-optional">generated for you</span>
                </span>
              </div>

              <div className="form-field">
                <label htmlFor="np-lang">Language</label>
                <select
                  id="np-lang"
                  value={langChoice}
                  onChange={(e) => setLangChoice(e.target.value)}
                >
                  {LANGUAGES.map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
                {usingOther && (
                  <>
                    <input
                      id="np-lang-other"
                      type="text"
                      placeholder="haskell"
                      value={otherLang}
                      onChange={(e) => setOtherLang(e.target.value)}
                    />
                    <span className="form-hint">
                      No built-in template for this language — the project starts as just
                      PROJECT.yaml and README, with the language recorded as-is.
                    </span>
                  </>
                )}
              </div>

              <div className="form-field">
                <label htmlFor="np-build-engine">Build system</label>
                <select
                  id="np-build-engine"
                  value={buildEngine}
                  onChange={(e) => setBuildEngine(e.target.value)}
                >
                  {BUILD_ENGINES.map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </div>
            </div>

            <div className="form-group">
              <div className="form-eyebrow"><span>Ownership</span></div>

              <div className="form-field">
                <label htmlFor="np-owners">
                  Owners <span className="form-optional">optional</span>
                </label>
                <input
                  id="np-owners"
                  type="text"
                  placeholder="group:commerce user:val"
                  value={ownersText}
                  onChange={(e) => setOwnersText(e.target.value)}
                />
                <span className="form-hint">
                  Space or comma separated. Empty inherits from the nearest OWNERS file or the
                  org default.
                </span>
              </div>
            </div>

            {createError && <ErrorNote error={createError} />}

            <button
              className="btn btn-primary"
              disabled={busy || apiMissing || !preview || !!previewError}
              onClick={() => void createProject()}
            >
              {busy ? "Creating…" : "Create as a change"}
            </button>
          </section>
        </div>

        <aside>
          <section className="card side-card">
            <h2>
              Preview{" "}
              <InfoTip text="The exact files the change will carry - nothing is written until you press create, and nothing reaches trunk until that change lands." />
            </h2>
            {previewing && <Spinner />}
            {previewError && <ErrorNote error={previewError} />}
            {!previewing && !previewError && !preview && (
              <EmptyState>
                {!name.trim()
                  ? "Type a name to see the generated files."
                  : apiMissing
                    ? "Pick an API surface to see the generated files."
                    : "Nothing to preview yet."}
              </EmptyState>
            )}
            {preview && (
              <>
                <p className="page-sub">
                  {preview.files.length} files under <span className="mono">{preview.path}/</span>
                </p>
                {preview.files.map((f) => (
                  <div className="preview-file" key={f.path}>
                    <div className="preview-file-path">
                      {preview.path}/{f.path}
                    </div>
                    <pre className="preview-file-body">{f.content.replace(/\n$/, "")}</pre>
                  </div>
                ))}
              </>
            )}
          </section>
        </aside>
      </div>
    </div>
  );
}
