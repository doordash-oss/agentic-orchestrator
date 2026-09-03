/**
 * The shared per-field validation message: the one error primitive that is
 * not a card. A single element carrying the id its input references through
 * `aria-describedby`, one class, and nothing else — no role, no live region
 * (the host focuses the field when its message appears, so announcing the
 * message again would be noise), no card chrome.
 *
 * Hosts wire their input with the companion helpers so both attributes stay
 * unset while the field is clean:
 *
 *   <input
 *     id={inputId}
 *     aria-describedby={fieldAriaDescribedBy(errorId, hasError)}
 *     aria-invalid={fieldAriaInvalid(hasError)}
 *   />
 *   <FieldError id={errorId} message={message} />
 */
export interface FieldErrorProps {
  /** The id the host input references via `aria-describedby`. */
  id: string;
  /** The validation message; nothing renders while it is empty. */
  message?: string | null;
}

/** The input's `aria-describedby` value: the message id only while an error shows. */
export function fieldAriaDescribedBy(id: string, hasError: boolean): string | undefined {
  return hasError ? id : undefined;
}

/** The input's `aria-invalid` value: set only while an error shows. */
export function fieldAriaInvalid(hasError: boolean): true | undefined {
  return hasError ? true : undefined;
}

export function FieldError({ id, message }: FieldErrorProps) {
  if (message == null || message === '') return null;
  return (
    <p id={id} className="field-error">
      {message}
    </p>
  );
}
