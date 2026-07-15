/**
 * The feature creation flow: name, description, repository selection
 * from fresh server discovery, branch choice, and the server-provided
 * defaults the creation contract applies. Client-side validation mirrors the
 * IPC schema; structured server rejections map onto the owning control with
 * focus + aria-describedby; the submit is single-flight. After a successful
 * create the durable setup action is dispatched explicitly — starting the
 * feature remains a later-phase, user-performed action.
 */
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import type { CreationDefaults } from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { fieldForCreationError } from './featureView';

type DefaultsState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; defaults: CreationDefaults };

export interface CreateFeatureFormProps {
  /** Fired once the durable feature exists (whether or not dispatch worked). */
  onCreated(created: { featureId: string; name: string }): void;
}

export function CreateFeatureForm({ onCreated }: CreateFeatureFormProps) {
  const [state, setState] = useState<DefaultsState>({ phase: 'loading' });
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [repoKeys, setRepoKeys] = useState<readonly string[]>([]);
  const [useCurrentBranch, setUseCurrentBranch] = useState(false);
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [repoError, setRepoError] = useState<string | null>(null);
  const [formError, setFormError] = useState<WizardError | null>(null);

  const nameRef = useRef<HTMLInputElement | null>(null);
  const repoGroupRef = useRef<HTMLFieldSetElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);

  const loadDefaults = useCallback(() => {
    setState({ phase: 'loading' });
    window.agentico
      .getCreationDefaults()
      .then((defaults) => {
        setState({ phase: 'loaded', defaults });
        // Prefill the server default branch choice.
        setUseCurrentBranch(defaults.defaults.useCurrentBranch);
      })
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  useEffect(() => {
    loadDefaults();
  }, [loadDefaults]);

  useEffect(() => {
    if (formError !== null) {
      formErrorRef.current?.focus();
    }
  }, [formError]);

  const toggleRepo = useCallback((key: string) => {
    setRepoKeys((current) =>
      current.includes(key) ? current.filter((k) => k !== key) : [...current, key],
    );
    setRepoError(null);
  }, []);

  const submit = useCallback(
    (event: FormEvent) => {
      event.preventDefault();
      if (pending || state.phase !== 'loaded') {
        return;
      }
      setNameError(null);
      setRepoError(null);
      setFormError(null);

      // Client-side validation mirrors the IPC CreateFeatureInputSchema.
      const trimmed = name.trim();
      if (trimmed === '' || trimmed.length > 200) {
        setNameError('Enter a feature name.');
        nameRef.current?.focus();
        return;
      }
      if (repoKeys.length === 0) {
        setRepoError('Select at least one repository.');
        repoGroupRef.current?.focus();
        return;
      }

      setPending(true);
      void (async () => {
        try {
          const created = await window.agentico.createFeature({
            name: trimmed,
            description,
            repoKeys: [...repoKeys],
            useCurrentBranch,
          });
          // Creation queues durable setup but never dispatches it — that is
          // this explicit follow-up action. If dispatch fails the feature
          // still exists; the cockpit shows the authoritative state with a
          // server-authorized retry.
          try {
            await window.agentico.dispatchFeatureSetup(created.featureId);
          } catch {
            // Surfaced by the cockpit from the authoritative snapshot.
          }
          onCreated({ featureId: created.featureId, name: trimmed });
        } catch (err) {
          const parsed = parseIpcError(err);
          const field = fieldForCreationError(parsed);
          if (field === 'name') {
            setNameError(parsed.message);
            nameRef.current?.focus();
          } else if (field === 'repos') {
            setRepoError(parsed.message);
            repoGroupRef.current?.focus();
          } else {
            setFormError(parsed);
          }
        } finally {
          setPending(false);
        }
      })();
    },
    [description, name, onCreated, pending, repoKeys, state.phase, useCurrentBranch],
  );

  if (state.phase === 'loading') {
    return (
      <section className="create-form" aria-label="Create a feature">
        <p role="status" aria-live="polite" className="create-form__loading">
          Loading creation defaults from the runtime…
        </p>
      </section>
    );
  }

  if (state.phase === 'error') {
    return (
      <section className="create-form" aria-label="Create a feature">
        <div role="alert" className="create-form__error">
          <span className="create-form__error-code">{state.error.code}</span>
          <p className="create-form__error-message">{state.error.message}</p>
        </div>
        <button type="button" className="setup-wizard__action" onClick={loadDefaults}>
          Try again
        </button>
      </section>
    );
  }

  const { repositories, defaults } = state.defaults;

  return (
    <form className="create-form" aria-label="Create a feature" noValidate onSubmit={submit}>
      <h2 className="setup-step__title">New feature</h2>

      {formError !== null ? (
        <div
          ref={formErrorRef}
          tabIndex={-1}
          role="alert"
          className="create-form__error"
          aria-label="Creation error"
        >
          <span className="create-form__error-code">{formError.code}</span>
          <p className="create-form__error-message">{formError.message}</p>
        </div>
      ) : null}

      <div className="form-field">
        <label className="form-field__label" htmlFor="feature-name">
          Name
        </label>
        <input
          ref={nameRef}
          id="feature-name"
          className="form-field__input"
          type="text"
          value={name}
          maxLength={200}
          required
          aria-required="true"
          aria-invalid={nameError !== null}
          {...(nameError !== null ? { 'aria-describedby': 'feature-name-error' } : {})}
          onChange={(event) => {
            setName(event.target.value);
            setNameError(null);
          }}
        />
        {nameError !== null ? (
          <p id="feature-name-error" className="form-field__error">
            {nameError}
          </p>
        ) : null}
      </div>

      <div className="form-field">
        <label className="form-field__label" htmlFor="feature-description">
          Description <span className="form-field__optional">(optional)</span>
        </label>
        <textarea
          id="feature-description"
          className="form-field__input form-field__input--multiline"
          value={description}
          maxLength={10000}
          rows={4}
          onChange={(event) => setDescription(event.target.value)}
        />
      </div>

      <fieldset
        ref={repoGroupRef}
        tabIndex={-1}
        className="form-field form-field--group"
        aria-describedby={repoError !== null ? 'feature-repos-error' : 'feature-repos-hint'}
        aria-invalid={repoError !== null}
      >
        <legend className="form-field__label">Repositories</legend>
        <p id="feature-repos-hint" className="setup-step__hint">
          Discovered in your workspace by the runtime just now.
        </p>
        {repositories.length === 0 ? (
          <p className="setup-step__empty">No repositories were discovered in the workspace.</p>
        ) : (
          <ul className="repo-options">
            {repositories.map((repository) => (
              <li key={repository.name} className="repo-option" data-valid={repository.valid}>
                <label className="repo-option__label">
                  <input
                    type="checkbox"
                    value={repository.name}
                    checked={repoKeys.includes(repository.name)}
                    disabled={!repository.valid || pending}
                    onChange={() => toggleRepo(repository.name)}
                  />
                  <span className="repo-option__name">{repository.name}</span>
                  <code className="repo-option__path">{repository.path}</code>
                </label>
                {!repository.valid ? (
                  <span className="repo-option__issue">
                    {repository.issue?.message ?? 'This repository is not usable.'}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        )}
        {repoError !== null ? (
          <p id="feature-repos-error" className="form-field__error">
            {repoError}
          </p>
        ) : null}
      </fieldset>

      <fieldset className="form-field form-field--group" role="radiogroup" aria-label="Branch">
        <legend className="form-field__label">Branch</legend>
        <label className="repo-option__label">
          <input
            type="radio"
            name="feature-branch"
            checked={!useCurrentBranch}
            onChange={() => setUseCurrentBranch(false)}
          />
          <span>New feature branch (server default)</span>
        </label>
        <label className="repo-option__label">
          <input
            type="radio"
            name="feature-branch"
            checked={useCurrentBranch}
            onChange={() => setUseCurrentBranch(true)}
          />
          <span>Use each repository&apos;s current branch</span>
        </label>
      </fieldset>

      <section className="defaults-panel" aria-label="Server defaults">
        <h3 className="defaults-panel__title">Server defaults</h3>
        <p className="setup-step__hint">
          Set when the feature is created. You can change it later.
        </p>
        <dl className="defaults-panel__facts">
          {defaults.pipeline !== undefined ? (
            <div className="defaults-panel__fact">
              <dt>Pipeline</dt>
              <dd>
                <code>{defaults.pipeline}</code>
              </dd>
            </div>
          ) : null}
          {defaults.inquireness !== undefined ? (
            <div className="defaults-panel__fact">
              <dt>Inquireness</dt>
              <dd>
                <code>{defaults.inquireness}</code>
              </dd>
            </div>
          ) : null}
          {defaults.models.map((entry) => (
            <div key={entry.phase} className="defaults-panel__fact">
              <dt>{entry.phase} model</dt>
              <dd>
                <code>{entry.model}</code>
              </dd>
            </div>
          ))}
        </dl>
      </section>

      <button type="submit" className="create-form__submit" disabled={pending}>
        {pending ? 'Creating…' : 'Create feature'}
      </button>
    </form>
  );
}
