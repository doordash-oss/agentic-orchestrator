import type { FeatureActionView } from '../../../shared/ipc';

/**
 * Server-owned impact projection rendered as a consequence ledger: what the
 * operation removes sits against a danger rail, what survives against a
 * success rail. Every category is still accounted for — empty ones collapse
 * into one quiet line so the dialog never implies hidden impact without
 * giving "None" the same weight as a real removal.
 */
export function ImpactPreviewList({
  preview,
}: {
  preview: NonNullable<FeatureActionView['impactPreview']>;
}): React.ReactElement {
  const removed = preview.categories.filter((category) => category.items.length > 0);
  const untouched = preview.categories.filter((category) => category.items.length === 0);
  const lede =
    preview.kind === 'child_discard'
      ? 'This deletes the pass’s local working copies. Everything under Kept stays with the parent.'
      : 'This deletes the feature and the local work listed below.';

  return (
    <div className="impact-dialog__preview">
      <p className="impact-dialog__lede">{lede}</p>
      {removed.length > 0 ? (
        <div className="impact-dialog__lane impact-dialog__lane--removed">
          {removed.map((category) => (
            <section key={category.key}>
              <h4>{category.label}</h4>
              <ul>
                {category.items.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      ) : null}
      <div className="impact-dialog__lane impact-dialog__lane--kept">
        <section>
          <h4>Kept</h4>
          {preview.retained.length === 0 ? (
            <p>None</p>
          ) : (
            <ul>
              {preview.retained.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          )}
        </section>
      </div>
      {untouched.length > 0 ? (
        <p className="impact-dialog__quiet">
          {untouched.map((category) => `${category.label}: none`).join(' · ')}
        </p>
      ) : null}
    </div>
  );
}
