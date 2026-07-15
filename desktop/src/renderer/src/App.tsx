import type { ThemePreference } from '../../shared/ipc';
import { ConnectionShell } from './components/ConnectionShell';
import { ReadinessGate } from './components/ReadinessGate';
import { useConnectionState, useTheme } from './hooks';

const THEME_OPTIONS: readonly { value: ThemePreference; label: string }[] = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'System' },
];

export default function App() {
  const { preference, setPreference } = useTheme();
  const connection = useConnectionState();

  return (
    <div className="app-frame">
      {connection.status === 'ready' ? <ReadinessGate /> : <ConnectionShell />}
      <fieldset className="theme-switcher" role="radiogroup" aria-label="Theme">
        <legend className="theme-switcher__legend">Theme</legend>
        {THEME_OPTIONS.map((option) => (
          <label key={option.value} className="theme-switcher__option">
            <input
              type="radio"
              name="theme"
              value={option.value}
              checked={preference === option.value}
              onChange={() => setPreference(option.value)}
            />
            <span>{option.label}</span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}
