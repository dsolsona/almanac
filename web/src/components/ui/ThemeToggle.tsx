import { useEffect, useState } from 'react';
import { SegmentedControl } from './SegmentedControl.tsx';

type Theme = 'light' | 'dark';
const storageKey = 'almanac-theme';

function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(storageKey);
    if (stored === 'light' || stored === 'dark') return stored;
  } catch { /* Storage can be disabled by browser policy. */ }
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function initializeTheme() {
  const theme = readTheme();
  document.documentElement.classList.toggle('dark', theme === 'dark');
  document.documentElement.style.colorScheme = theme;
}

function useTheme() {
  const [theme, setTheme] = useState<Theme>(readTheme);
  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    document.documentElement.style.colorScheme = theme;
    try { localStorage.setItem(storageKey, theme); } catch { /* Keep the in-memory choice for this session. */ }
  }, [theme]);
  return [theme, setTheme] as const;
}

export function ThemeChoice() {
  const [theme,setTheme] = useTheme();
  return <div className="ui-field"><span className="ui-label">Theme</span><SegmentedControl label="Theme" value={theme} onChange={setTheme} options={[{value:'light',label:'Light'},{value:'dark',label:'Dark'}]}/></div>;
}

export function ThemeToggle() {
  const [theme,setTheme] = useTheme();
  return <button className="theme-toggle" type="button" aria-pressed={theme === 'dark'} aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`} onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
    <span aria-hidden="true">{theme === 'dark' ? '☾' : '☼'}</span><span>{theme === 'dark' ? 'Dark' : 'Light'} theme</span>
  </button>;
}
