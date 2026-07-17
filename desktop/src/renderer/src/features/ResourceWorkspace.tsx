import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type * as Monaco from 'monaco-editor';
import {
  DEFAULT_RUNTIME_ID,
  type ResourceCatalogue,
  type ResourceDraftKey,
  type ResourceEntry,
  type ResourceFinding,
  type ResourceRead,
  type ResourceValidateResult,
  type ResourceWriteResult,
} from '../../../shared/ipc';
import { useMediaQuery } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';

type ThemeMode = 'light' | 'dark';
type EditorState =
  | 'idle'
  | 'loading'
  | 'dirty'
  | 'validating'
  | 'invalid'
  | 'saving'
  | 'saved'
  | 'stale'
  | 'recovered'
  | 'failed';

interface ReconcileState {
  currentText: string;
  currentRevision: string;
  localText: string;
  localKey: ResourceDraftKey;
}

const MONACO_LIGHT_THEME = 'vs';
const MONACO_DARK_THEME = 'vs-dark';

const EFFECT_LABELS: Record<string, string> = {
  immediate: 'Takes effect immediately',
  next_dispatch: 'Applies to the next dispatch',
  next_session: 'Available to subsequent reads and new sessions',
  restart_required: 'Requires a restart to take effect',
};

/**
 * Runtime config has mixed effect timing: workspace-root changes refresh
 * discovery immediately while most other fields apply to the next dispatch.
 * The blanket server effect is `next_dispatch`, so the label is refined
 * here to avoid contradicting the workspace-roots section above it.
 */
function effectLabelFor(kind: string, effect: string): string {
  if (kind === 'runtime_config' && effect === 'next_dispatch') {
    return 'Most fields apply to the next dispatch; workspace roots refresh immediately';
  }
  return EFFECT_LABELS[effect] ?? effect;
}

const KIND_LABELS: Record<string, string> = {
  feature_config: 'Features',
  runtime_config: 'Runtime',
  skill: 'Skills',
  guideline: 'Guidelines',
};

const KIND_ORDER = ['feature_config', 'runtime_config', 'skill', 'guideline'];

const STATE_LABELS: Record<EditorState, string> = {
  idle: 'Saved',
  loading: 'Loading…',
  dirty: 'Unsaved changes',
  validating: 'Validating…',
  invalid: 'Invalid content',
  saving: 'Saving…',
  saved: 'Saved',
  stale: 'Stale — reconcile needed',
  recovered: 'Recovered draft',
  failed: 'Failed',
};

export function useResolvedTheme(): ThemeMode {
  const [theme, setTheme] = useState<ThemeMode>(() =>
    document.documentElement.dataset['theme'] === 'dark' ? 'dark' : 'light',
  );
  useEffect(() => {
    const observer = new MutationObserver(() => {
      setTheme(document.documentElement.dataset['theme'] === 'dark' ? 'dark' : 'light');
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    });
    return () => observer.disconnect();
  }, []);
  return theme;
}

function languageForContentType(ct: string): string {
  switch (ct) {
    case 'yaml':
      return 'yaml';
    case 'markdown':
      return 'markdown';
    default:
      return 'plaintext';
  }
}

function MonacoBuffer({
  defaultValue,
  language,
  theme,
  readOnly,
  onChange,
}: {
  defaultValue: string;
  language: string;
  theme: ThemeMode;
  readOnly: boolean;
  onChange(value: string): void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const readOnlyRef = useRef(readOnly);
  readOnlyRef.current = readOnly;

  useEffect(() => {
    let disposed = false;
    void import('monaco-editor').then((monaco) => {
      if (disposed || host.current === null) return;
      editor.current = monaco.editor.create(host.current, {
        value: defaultValue,
        language,
        theme: theme === 'dark' ? MONACO_DARK_THEME : MONACO_LIGHT_THEME,
        minimap: { enabled: false },
        automaticLayout: true,
        wordWrap: 'on',
        fontFamily: 'IBM Plex Mono, monospace',
        fontSize: 14,
        scrollBeyondLastLine: false,
        readOnly: readOnlyRef.current,
        ariaLabel: 'Resource editor',
      });
      // Expose for e2e tests: setValue bypasses Monaco auto-indent that
      // corrupts multi-line YAML when text is inserted via the textarea.
      (host.current as unknown as Record<string, unknown>).__monacoEditor = editor.current;
      editor.current.onDidChangeModelContent(() =>
        onChangeRef.current(editor.current?.getValue() ?? ''),
      );
    });
    return () => {
      disposed = true;
      editor.current?.dispose();
      editor.current = null;
    };
  }, []);

  useEffect(() => {
    if (editor.current === null) return;
    editor.current.updateOptions({ readOnly });
  }, [readOnly]);

  useEffect(() => {
    if (editor.current === null) return;
    void import('monaco-editor').then((monaco) => {
      monaco.editor.setTheme(theme === 'dark' ? MONACO_DARK_THEME : MONACO_LIGHT_THEME);
    });
  }, [theme]);

  return <div className="resource-editor__monaco" ref={host} />;
}

function MonacoDiff({
  localText,
  currentText,
  language,
  theme,
}: {
  localText: string;
  currentText: string;
  language: string;
  theme: ThemeMode;
}) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let disposed = false;
    let diff: Monaco.editor.IStandaloneDiffEditor | null = null;
    let original: Monaco.editor.ITextModel | null = null;
    let modified: Monaco.editor.ITextModel | null = null;
    void import('monaco-editor').then((monaco) => {
      if (disposed || host.current === null) return;
      original = monaco.editor.createModel(currentText, language);
      modified = monaco.editor.createModel(localText, language);
      diff = monaco.editor.createDiffEditor(host.current, {
        automaticLayout: true,
        readOnly: true,
        minimap: { enabled: false },
        renderSideBySide: true,
        theme: theme === 'dark' ? MONACO_DARK_THEME : MONACO_LIGHT_THEME,
        ariaLabel: 'Your draft compared with the current server content',
      });
      diff.setModel({ original, modified });
    });
    return () => {
      disposed = true;
      diff?.dispose();
      original?.dispose();
      modified?.dispose();
    };
  }, [currentText, localText, language, theme]);
  return (
    <div className="resource-editor__diff-wrapper">
      <div className="resource-editor__diff-panes">
        <span className="resource-editor__diff-pane-label">Server content</span>
        <span className="resource-editor__diff-pane-label">Your draft</span>
      </div>
      <div
        className="resource-editor__diff"
        ref={host}
        aria-label="Your draft compared with the current server content"
      />
    </div>
  );
}

interface TreeNode {
  label: string;
  entry?: ResourceEntry;
  children: Map<string, TreeNode>;
  kind?: string;
}

function buildTree(entries: ResourceEntry[], kind: string): TreeNode {
  const root: TreeNode = { label: '', children: new Map(), kind };
  for (const entry of entries) {
    const fullpath = entry.hierarchy ?? [entry.label];
    const path = fullpath.length > 1 ? fullpath.slice(1) : fullpath;
    let node = root;
    for (let i = 0; i < path.length; i++) {
      const segment = path[i]!;
      let child = node.children.get(segment);
      if (child === undefined) {
        child = { label: segment, children: new Map() };
        node.children.set(segment, child);
      }
      if (i === path.length - 1) {
        child.entry = entry;
      }
      node = child;
    }
  }
  return root;
}

function TreeGroup({
  node,
  depth,
  pathPrefix,
  kindPrefix,
  selectedId,
  onSelect,
  expanded,
  onToggle,
}: {
  node: TreeNode;
  depth: number;
  pathPrefix: string;
  kindPrefix: string;
  selectedId: string | null;
  onSelect(id: string): void;
  expanded: Set<string>;
  onToggle(key: string): void;
}) {
  const children = Array.from(node.children.values()).sort((a, b) => {
    if (a.entry && !b.entry) return 1;
    if (!a.entry && b.entry) return -1;
    return a.label.localeCompare(b.label);
  });
  return (
    <>
      {children.map((child) => {
        const hasChildren = child.children.size > 0;
        const treeKey = pathPrefix
          ? `${pathPrefix}/${child.label}`
          : `${kindPrefix}:${child.label}`;
        const isExpanded = expanded.has(treeKey);
        if (hasChildren) {
          return (
            <div key={treeKey} className="resource-browser__tree-node">
              <button
                type="button"
                className="resource-browser__tree-toggle"
                onClick={() => onToggle(treeKey)}
                aria-expanded={isExpanded}
                style={{ paddingLeft: `${8 + depth * 16}px` }}
              >
                <span aria-hidden="true">{isExpanded ? '▾' : '▸'}</span>
                {child.label}
              </button>
              {isExpanded && (
                <TreeGroup
                  node={child}
                  depth={depth + 1}
                  pathPrefix={treeKey}
                  kindPrefix={kindPrefix}
                  selectedId={selectedId}
                  onSelect={onSelect}
                  expanded={expanded}
                  onToggle={onToggle}
                />
              )}
            </div>
          );
        }
        if (child.entry) {
          return (
            <button
              key={child.entry.id}
              className={`resource-browser__entry ${selectedId === child.entry.id ? 'is-selected' : ''}`}
              onClick={() => onSelect(child.entry!.id)}
              role="option"
              aria-selected={selectedId === child.entry.id}
              style={{ paddingLeft: `${8 + depth * 16}px` }}
            >
              <span className="resource-browser__entry-label">{child.label}</span>
            </button>
          );
        }
        return null;
      })}
    </>
  );
}

function ResourceBrowser({
  entries,
  selectedId,
  onSelect,
  search,
  onSearch,
  kindFilter,
  onKindFilter,
  truncated,
}: {
  entries: ResourceEntry[];
  selectedId: string | null;
  onSelect(id: string): void;
  search: string;
  onSearch(value: string): void;
  kindFilter: string;
  onKindFilter(kind: string): void;
  truncated?: boolean;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [autoExpanded, setAutoExpanded] = useState(false);

  const filtered = useMemo(() => {
    const lower = search.toLowerCase();
    return entries.filter((e) => {
      if (kindFilter && e.kind !== kindFilter) return false;
      if (lower) {
        const haystack = [e.label, ...(e.hierarchy ?? [])].join(' ').toLowerCase();
        if (!haystack.includes(lower)) return false;
      }
      return true;
    });
  }, [entries, search, kindFilter]);

  const grouped = useMemo(() => {
    const map = new Map<string, ResourceEntry[]>();
    for (const e of filtered) {
      const arr = map.get(e.kind) ?? [];
      arr.push(e);
      map.set(e.kind, arr);
    }
    return map;
  }, [filtered]);

  const trees = useMemo(() => {
    const map = new Map<string, TreeNode>();
    for (const kind of KIND_ORDER) {
      const kindEntries = grouped.get(kind);
      if (kindEntries) {
        map.set(kind, buildTree(kindEntries, kind));
      }
    }
    return map;
  }, [grouped]);

  useEffect(() => {
    if (autoExpanded || trees.size === 0) return;
    const firstLevelKeys: string[] = [];
    for (const [kind, tree] of trees) {
      for (const child of tree.children.keys()) {
        firstLevelKeys.push(`${kind}:${child}`);
      }
    }
    if (firstLevelKeys.length > 0) {
      setExpanded((prev) => new Set([...prev, ...firstLevelKeys]));
      setAutoExpanded(true);
    }
  }, [trees, autoExpanded]);

  const toggleNode = useCallback((key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const emptyMessage = useMemo(() => {
    if (kindFilter === 'feature_config')
      return 'Feature configurations appear once a feature exists.';
    if (kindFilter === 'runtime_config')
      return 'Runtime configuration is available when the server is connected.';
    if (kindFilter === 'skill' || kindFilter === 'guideline')
      return 'No matching skill or guideline files found.';
    return 'No resources match the current filter.';
  }, [kindFilter]);

  return (
    <nav className="resource-browser" aria-label="Resource catalogue">
      <div className="resource-browser__filters">
        <input
          className="resource-browser__search"
          type="search"
          placeholder="Filter resources…"
          value={search}
          onChange={(e) => onSearch(e.target.value)}
          aria-label="Filter resources"
        />
        <div className="resource-browser__kinds" role="group" aria-label="Filter by kind">
          <button
            className={`resource-browser__kind-btn ${kindFilter === '' ? 'is-active' : ''}`}
            onClick={() => onKindFilter('')}
            aria-pressed={kindFilter === ''}
          >
            All
          </button>
          {KIND_ORDER.map((kind) => (
            <button
              key={kind}
              className={`resource-browser__kind-btn ${kindFilter === kind ? 'is-active' : ''}`}
              onClick={() => onKindFilter(kind)}
              aria-pressed={kindFilter === kind}
            >
              {KIND_LABELS[kind] ?? kind}
            </button>
          ))}
        </div>
      </div>
      <div className="resource-browser__list" role="listbox" aria-label="Resources">
        {KIND_ORDER.filter((kind) => trees.has(kind)).map((kind) => {
          const tree = trees.get(kind)!;
          return (
            <div key={kind} className="resource-browser__group">
              <div className="resource-browser__group-label">{KIND_LABELS[kind] ?? kind}</div>
              <TreeGroup
                node={tree}
                depth={0}
                pathPrefix=""
                kindPrefix={kind}
                selectedId={selectedId}
                onSelect={onSelect}
                expanded={expanded}
                onToggle={toggleNode}
              />
            </div>
          );
        })}
        {filtered.length === 0 && <p className="resource-browser__empty">{emptyMessage}</p>}
        {truncated && (
          <p className="resource-browser__truncated" role="status">
            Catalogue truncated — showing the first {entries.length} resources.
          </p>
        )}
      </div>
    </nav>
  );
}

function FindingsList({ findings }: { findings: ResourceFinding[] }) {
  if (findings.length === 0) return null;
  return (
    <ul className="resource-editor__findings" role="alert">
      {findings.map((f, i) => (
        <li key={i} className="resource-editor__finding">
          {f.field && <span className="resource-editor__finding-field">{f.field}: </span>}
          {f.message}
          {f.code && <span className="resource-editor__finding-code"> ({f.code})</span>}
        </li>
      ))}
    </ul>
  );
}

export function ResourceEditor({ resourceId, theme }: { resourceId: string; theme: ThemeMode }) {
  const [resource, setResource] = useState<ResourceRead | null>(null);
  const [text, setText] = useState('');
  const [baseText, setBaseText] = useState('');
  const [baseRevision, setBaseRevision] = useState('');
  const [runtimeId, setRuntimeId] = useState(DEFAULT_RUNTIME_ID);
  const [validation, setValidation] = useState<ResourceValidateResult | null>(null);
  const [notice, setNotice] = useState('Loading resource…');
  const [busy, setBusy] = useState(false);
  const [recovered, setRecovered] = useState(false);
  const [recoveredKey, setRecoveredKey] = useState<ResourceDraftKey | null>(null);
  const [reconcile, setReconcile] = useState<ReconcileState | null>(null);
  const [editorKey, setEditorKey] = useState(0);
  const [state, setState] = useState<EditorState>('loading');
  const [saveFindings, setSaveFindings] = useState<ResourceFinding[]>([]);

  useEffect(() => {
    let alive = true;
    setState('loading');
    setResource(null);
    setText('');
    setBaseText('');
    setValidation(null);
    setReconcile(null);
    setRecovered(false);
    setRecoveredKey(null);
    setSaveFindings([]);
    setNotice('Loading resource…');
    void Promise.all([window.agentico.readResource(resourceId), window.agentico.getSettings()])
      .then(async ([res, settings]) => {
        const rt = settings.runtime.selection ?? DEFAULT_RUNTIME_ID;
        if (!alive) return;
        setRuntimeId(rt);
        setResource(res);
        setBaseText(res.text);
        setBaseRevision(res.revision);
        const local = await window.agentico.loadLocalResourceDraft({
          runtimeId: rt,
          resourceId,
        });
        if (!alive) return;
        if (local === null) {
          setText(res.text);
          setState('idle');
          setNotice('Server content loaded.');
          return;
        }
        const draftKey: ResourceDraftKey = {
          runtimeId: rt,
          resourceId,
          baseRevision: local.baseRevision,
        };
        if (local.baseRevision !== res.revision) {
          setText(local.text);
          setRecovered(true);
          setRecoveredKey(draftKey);
          setReconcile({
            currentText: res.text,
            currentRevision: res.revision,
            localText: local.text,
            localKey: draftKey,
          });
          setState('stale');
          setNotice('Recovered draft needs reconciliation with newer server content.');
          return;
        }
        setText(local.text);
        setRecovered(true);
        setRecoveredKey(draftKey);
        setState('recovered');
        setNotice('Recovered unsaved draft loaded.');
      })
      .catch((error: unknown) => {
        if (!alive) return;
        setState('failed');
        setNotice(parseIpcError(error).message);
      });
    return () => {
      alive = false;
    };
  }, [resourceId]);

  const dirty = resource !== null && text !== baseText;
  const language = resource ? languageForContentType(resource.contentType) : 'plaintext';

  useEffect(() => {
    if (resource === null || !dirty || reconcile !== null) return;
    setState('validating');
    let active = true;
    const timer = window.setTimeout(() => {
      void window.agentico
        .validateResource({ resourceId, text })
        .then((result) => {
          if (!active) return;
          setValidation(result);
          setState(result.valid ? 'dirty' : 'invalid');
        })
        .catch(() => {
          if (!active) return;
          setValidation(null);
          setState('dirty');
        });
    }, 250);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [resource, resourceId, text, dirty, reconcile]);

  useEffect(() => {
    if (resource === null || !dirty || reconcile !== null) return;
    const timer = window.setTimeout(() => {
      void window.agentico
        .saveLocalResourceDraft({ runtimeId, resourceId, baseRevision, text })
        .catch(() => {});
    }, 350);
    return () => window.clearTimeout(timer);
  }, [dirty, reconcile, runtimeId, resourceId, baseRevision, text, resource]);

  useEffect(() => {
    if (reconcile !== null) return;
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        e.stopPropagation();
        if (dirty && !busy) void save();
      }
    };
    window.addEventListener('keydown', handler, true);
    return () => window.removeEventListener('keydown', handler, true);
  });

  const discardDraft = useCallback(
    async (key?: ResourceDraftKey | null): Promise<void> => {
      const draftKey = key ?? recoveredKey;
      if (draftKey) {
        await window.agentico.discardLocalResourceDraft(draftKey);
      }
      setRecovered(false);
      setRecoveredKey(null);
    },
    [recoveredKey],
  );

  const writeAndApply = useCallback(
    async (
      writeText: string,
      writeBaseRevision: string,
      draftKey: ResourceDraftKey | null,
      successNotice: string,
    ): Promise<void> => {
      if (resource === null) return;
      setBusy(true);
      setSaveFindings([]);
      setState('saving');
      setNotice('Saving…');
      try {
        const result: ResourceWriteResult = await window.agentico.writeResource({
          resourceId,
          baseRevision: writeBaseRevision,
          text: writeText,
        });
        if (result.type === 'conflict') {
          const conflictKey: ResourceDraftKey = draftKey ?? {
            runtimeId,
            resourceId,
            baseRevision: writeBaseRevision,
          };
          setReconcile({
            currentText: result.currentText,
            currentRevision: result.currentRevision,
            localText: writeText,
            localKey: conflictKey,
          });
          setState('stale');
          setNotice('The resource changed elsewhere. Choose how to reconcile.');
          return;
        }
        setText(writeText);
        setBaseText(writeText);
        setBaseRevision(result.revision);
        setEditorKey((k) => k + 1);
        setState('saved');
        setNotice(successNotice);
        await discardDraft(draftKey);
        setResource((prev) =>
          prev ? { ...prev, revision: result.revision, text: writeText } : prev,
        );
      } catch (error) {
        const parsed = parseIpcError(error);
        setState('failed');
        setNotice(`Save failed — ${parsed.message}`);
        try {
          const revalidated = await window.agentico.validateResource({
            resourceId,
            text: writeText,
          });
          setSaveFindings(revalidated.findings);
        } catch {
          setSaveFindings([]);
        }
      } finally {
        setBusy(false);
      }
    },
    [resource, resourceId, runtimeId, discardDraft],
  );

  const save = useCallback(async () => {
    if (resource === null || busy) return;
    const draftKey: ResourceDraftKey = recoveredKey ?? {
      runtimeId,
      resourceId,
      baseRevision,
    };
    await writeAndApply(text, baseRevision, draftKey, 'Saved.');
  }, [resource, busy, text, baseRevision, recoveredKey, runtimeId, resourceId, writeAndApply]);

  const takeCurrent = useCallback(async () => {
    if (reconcile === null) return;
    setText(reconcile.currentText);
    setBaseText(reconcile.currentText);
    setBaseRevision(reconcile.currentRevision);
    setReconcile(null);
    setEditorKey((k) => k + 1);
    setState('idle');
    setNotice('Server content loaded.');
    await discardDraft(reconcile.localKey);
  }, [reconcile, discardDraft]);

  const replaceWithMine = useCallback(async () => {
    if (reconcile === null) return;
    await writeAndApply(
      reconcile.localText,
      reconcile.currentRevision,
      reconcile.localKey,
      'Your draft replaced the server content.',
    );
  }, [reconcile, writeAndApply]);

  const continueEditing = useCallback(() => {
    if (reconcile === null) return;
    setReconcile(null);
    setState(dirty ? 'dirty' : 'idle');
    setNotice('Continuing with your local draft.');
  }, [reconcile, dirty]);

  if (resource === null && state === 'loading') {
    return (
      <div className="resource-editor resource-editor--placeholder">
        <p className="resource-editor__notice" role="status">
          {notice}
        </p>
      </div>
    );
  }

  if (resource === null && state === 'failed') {
    return (
      <div className="resource-editor resource-editor--placeholder">
        <p className="resource-editor__notice resource-editor__notice--error" role="alert">
          {notice}
        </p>
      </div>
    );
  }

  if (resource === null) {
    return (
      <div className="resource-editor resource-editor--placeholder">
        <p className="resource-editor__notice">Select a resource to edit.</p>
      </div>
    );
  }

  const canSave = dirty && !busy && reconcile === null && (validation?.valid ?? true);

  return (
    <div className="resource-editor">
      <header className="resource-editor__header">
        <div className="resource-editor__breadcrumb">
          {resource.hierarchy && resource.hierarchy.length > 0
            ? resource.hierarchy.join(' / ')
            : resource.label}
        </div>
        <div className="resource-editor__meta">
          {resource.effect && (
            <span
              className="resource-editor__effect"
              title={effectLabelFor(resource.kind, resource.effect)}
            >
              {effectLabelFor(resource.kind, resource.effect)}
            </span>
          )}
          <span className={`resource-editor__state resource-editor__state--${state}`} role="status">
            {STATE_LABELS[state]}
          </span>
        </div>
      </header>

      {reconcile !== null ? (
        <div className="resource-editor__reconcile">
          <p className="resource-editor__reconcile-notice" role="status">
            {notice}
          </p>
          <MonacoDiff
            localText={reconcile.localText}
            currentText={reconcile.currentText}
            language={language}
            theme={theme}
          />
          <div className="resource-editor__reconcile-actions">
            <button
              className="resource-editor__btn resource-editor__btn--primary"
              onClick={() => void takeCurrent()}
              disabled={busy}
            >
              Take current
            </button>
            <button
              className="resource-editor__btn resource-editor__btn--accent"
              onClick={() => void replaceWithMine()}
              disabled={busy}
            >
              Replace with mine
            </button>
            <button
              className="resource-editor__btn"
              onClick={() => continueEditing()}
              disabled={busy}
            >
              Continue editing
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="resource-editor__body">
            <MonacoBuffer
              key={editorKey}
              defaultValue={text}
              language={language}
              theme={theme}
              readOnly={state === 'loading'}
              onChange={setText}
            />
          </div>
          <FindingsList findings={validation?.findings ?? []} />
          <FindingsList findings={saveFindings} />
          <footer className="resource-editor__footer">
            <span className="resource-editor__notice" role="status">
              {notice}
            </span>
            <div className="resource-editor__actions">
              {recovered && (
                <button
                  className="resource-editor__btn"
                  onClick={() =>
                    void discardDraft().then(() => {
                      setText(baseText);
                      setEditorKey((k) => k + 1);
                      setSaveFindings([]);
                      setNotice('Recovered draft discarded.');
                    })
                  }
                  disabled={busy}
                >
                  Discard draft
                </button>
              )}
              <button
                className="resource-editor__btn resource-editor__btn--primary"
                onClick={() => void save()}
                disabled={!canSave}
              >
                Save
              </button>
            </div>
          </footer>
        </>
      )}
    </div>
  );
}

export function ResourceWorkspace() {
  const [catalogue, setCatalogue] = useState<ResourceCatalogue | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [error, setError] = useState<string | null>(null);
  const theme = useResolvedTheme();
  const isNarrow = useMediaQuery('(max-width: 900px)');

  const loadCatalogue = useCallback(() => {
    void window.agentico
      .listResources()
      .then((cat) => {
        setCatalogue(cat);
        setError(null);
      })
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
  }, []);

  useEffect(() => {
    loadCatalogue();
  }, [loadCatalogue]);

  useEffect(() => {
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type === 'invalidated') {
        const kind = event.kind;
        if (
          kind === 'resync' ||
          kind.startsWith('feature.') ||
          kind.startsWith('config.') ||
          kind.startsWith('resource.')
        ) {
          loadCatalogue();
        }
      }
    });
    return unsub;
  }, [loadCatalogue]);

  const entries = catalogue?.resources ?? [];

  return (
    <section className={`resource-workspace ${isNarrow ? 'resource-workspace--narrow' : ''}`}>
      <ResourceBrowser
        entries={entries}
        selectedId={selectedId}
        onSelect={setSelectedId}
        search={search}
        onSearch={setSearch}
        kindFilter={kindFilter}
        onKindFilter={setKindFilter}
        truncated={catalogue?.truncated}
      />
      {selectedId ? (
        <ResourceEditor key={selectedId} resourceId={selectedId} theme={theme} />
      ) : (
        <div className="resource-editor resource-editor--placeholder">
          {error ? (
            <p className="resource-editor__notice resource-editor__notice--error" role="alert">
              {error}
            </p>
          ) : entries.length === 0 ? (
            <p className="resource-editor__notice">No editable resources found.</p>
          ) : (
            <p className="resource-editor__notice">Select a resource to edit.</p>
          )}
        </div>
      )}
    </section>
  );
}
