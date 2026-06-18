import { useState } from 'react';
import { IconButton } from '@/ui/Button';
import { MoonIcon, SunIcon } from '@/ui/icons';

type Theme = 'dark' | 'light';

const STORAGE_KEY = 'dl.theme';

/** Flips :root[data-theme] and persists the choice. DevLab defaults to dark; index.html
 *  restores a saved theme before first paint to avoid a flash. */
export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(
    () => (document.documentElement.getAttribute('data-theme') as Theme) || 'dark',
  );

  const toggle = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    try {
      localStorage.setItem(STORAGE_KEY, next);
    } catch {
      /* ignore storage errors */
    }
    setTheme(next);
  };

  return (
    <IconButton label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={toggle}>
      {theme === 'dark' ? <SunIcon className="h-4 w-4" /> : <MoonIcon className="h-4 w-4" />}
    </IconButton>
  );
}
