import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from './FieldError';

describe('FieldError', () => {
  it('renders the message as a single element carrying the given id', () => {
    render(<FieldError id="feature-name-error" message="Enter a feature name." />);
    const message = screen.getByText('Enter a feature name.');
    expect(message).toHaveAttribute('id', 'feature-name-error');
    expect(message).toHaveClass('field-error');
    expect(message.tagName).toBe('P');
  });

  it('renders nothing without a message', () => {
    const { container } = render(<FieldError id="feature-name-error" message={undefined} />);
    expect(container.querySelector('.field-error')).toBeNull();
  });

  it('exposes the message as its input description through the aria wiring', () => {
    render(
      <>
        <label htmlFor="feature-name">Feature name</label>
        <input
          id="feature-name"
          aria-describedby={fieldAriaDescribedBy('feature-name-error', true)}
          aria-invalid={fieldAriaInvalid(true)}
        />
        <FieldError id="feature-name-error" message="Enter a feature name." />
      </>,
    );
    const input = screen.getByRole('textbox', { name: 'Feature name' });
    expect(input).toHaveDescription('Enter a feature name.');
    expect(input).toHaveAttribute('aria-invalid', 'true');
  });

  it('sets no live-region role on the message element', () => {
    render(<FieldError id="feature-name-error" message="Enter a feature name." />);
    expect(screen.getByText('Enter a feature name.')).not.toHaveAttribute('role');
  });

  it('leaves both helpers unset while no error is showing', () => {
    expect(fieldAriaDescribedBy('feature-name-error', false)).toBeUndefined();
    expect(fieldAriaInvalid(false)).toBeUndefined();
  });
});
