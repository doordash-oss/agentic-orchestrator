/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { useEffect, useRef, useState } from 'react';
import type * as Monaco from 'monaco-editor';

export type ThemeMode = 'light' | 'dark';

const MONACO_LIGHT_THEME = 'vs';
const MONACO_DARK_THEME = 'vs-dark';

function monacoThemeFor(theme: ThemeMode): string {
  return theme === 'dark' ? MONACO_DARK_THEME : MONACO_LIGHT_THEME;
}

/**
 * Tracks the resolved theme on <html data-theme> without duplicating the
 * MutationObserver listener across renderer features.
 */
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

/**
 * Monaco editor wrapper.
 *
 * `defaultValue` is read only at mount. To reset the editor's content
 * (after save, discard, or reconcile resolution), change the `key` prop
 * so React remounts a fresh instance. Do not call `setValue` after mount
 * — it fires `onDidChangeModelContent` during typing and corrupts state.
 *
 * When `exposeForE2E` is set, the editor instance is attached to the host
 * element as `__monacoEditor` so e2e tests can call `setValue` and bypass
 * Monaco's auto-indent, which corrupts multi-line YAML inserted via the
 * textarea.
 */
export function MonacoBuffer({
  defaultValue,
  language,
  theme,
  readOnly = false,
  ariaLabel,
  className,
  exposeForE2E = false,
  onChange,
}: {
  defaultValue: string;
  language: string;
  theme: ThemeMode;
  readOnly?: boolean;
  ariaLabel: string;
  className: string;
  exposeForE2E?: boolean;
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
        theme: monacoThemeFor(theme),
        minimap: { enabled: false },
        automaticLayout: true,
        wordWrap: 'on',
        // Monaco takes a plain font list, not a custom property, so the Bench
        // mono stack is spelled out here to match --bench-font-mono.
        fontFamily: 'ui-monospace, SF Mono, Menlo, monospace',
        fontSize: 14,
        scrollBeyondLastLine: false,
        readOnly: readOnlyRef.current,
        ariaLabel,
      });
      if (exposeForE2E) {
        (host.current as unknown as Record<string, unknown>).__monacoEditor = editor.current;
      }
      editor.current.onDidChangeModelContent(() =>
        onChangeRef.current(editor.current?.getValue() ?? ''),
      );
    });
    return () => {
      disposed = true;
      editor.current?.dispose();
      editor.current = null;
    };
    // The editor is created once; theme/readOnly changes are handled by the
    // effects below, and language/ariaLabel changes remount via `key`.
  }, []);

  useEffect(() => {
    if (editor.current === null) return;
    editor.current.updateOptions({ readOnly });
  }, [readOnly]);

  useEffect(() => {
    if (editor.current === null) return;
    void import('monaco-editor').then((monaco) => {
      monaco.editor.setTheme(monacoThemeFor(theme));
    });
  }, [theme]);

  return <div className={className} ref={host} />;
}

/**
 * Read-only side-by-side Monaco diff editor. The host div is rendered with
 * the supplied `className` and `ariaLabel`; callers that need surrounding
 * chrome (pane labels, wrappers) compose it around this component.
 */
export function MonacoDiff({
  localText,
  currentText,
  language,
  theme,
  ariaLabel,
  className,
}: {
  localText: string;
  currentText: string;
  language: string;
  theme: ThemeMode;
  ariaLabel: string;
  className: string;
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
        theme: monacoThemeFor(theme),
        ariaLabel,
      });
      diff.setModel({ original, modified });
    });
    return () => {
      disposed = true;
      diff?.dispose();
      original?.dispose();
      modified?.dispose();
    };
  }, [currentText, localText, language, theme, ariaLabel]);
  return <div className={className} ref={host} aria-label={ariaLabel} />;
}
