import type { FeatureActionView } from '../../../shared/ipc';

/**
 * Server-owned impact projection rendered verbatim: every category is always
 * present ("None" for empty) so the dialog never implies hidden impact, and
 * the retained list states what survives the operation.
 */
export function ImpactPreviewList({
  preview,
}: {
  preview: NonNullable<FeatureActionView['impactPreview']>;
}): React.ReactElement {
  return (
    <div className="impact-dialog__preview">
      {preview.categories.map((category) => (
        <section key={category.key}>
          <h4>{category.label}</h4>
          {category.items.length === 0 ? (
            <p>None</p>
          ) : (
            <ul>
              {category.items.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          )}
        </section>
      ))}
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
  );
}
