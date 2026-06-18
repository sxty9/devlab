import { useState } from 'react';
import { IconButton } from '@/ui/Button';
import { MoonIcon, SunIcon } from '@/ui/icons';

type Theme = 'dark' | 'light';

/** Flips :root[data-theme]; DevLab defaults to dark (set in index.html). */
export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(
    () => (document.documentElement.getAttribute('data-theme') as Theme) || 'dark',
  );

  const toggle = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    setTheme(next);
  };

  return (
    <IconButton label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={toggle}>
      {theme === 'dark' ? <SunIcon className="h-4 w-4" /> : <MoonIcon className="h-4 w-4" />}
    </IconButton>
  );
}
