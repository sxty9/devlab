import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
type Size = 'sm' | 'md';

const base =
  'inline-flex items-center justify-center gap-1.5 rounded-md font-medium select-none ' +
  'transition duration-fast ease-out focus-visible:outline-none focus-visible:ring-2 ' +
  'focus-visible:ring-accent/50 disabled:cursor-not-allowed disabled:opacity-40';

const variants: Record<Variant, string> = {
  primary: 'bg-accent text-accent-fg shadow-elev-1 hover:bg-accent-hover active:scale-[0.98]',
  secondary: 'bg-fill/10 text-text-primary hover:bg-fill/15 active:scale-[0.98]',
  ghost: 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
  danger: 'bg-danger text-accent-fg shadow-elev-1 hover:opacity-90 active:scale-[0.98]',
};

const sizes: Record<Size, string> = {
  sm: 'h-7 px-2.5 text-caption',
  md: 'h-9 px-3.5 text-subhead',
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', className, type = 'button', ...rest },
  ref,
) {
  return <button ref={ref} type={type} className={cn(base, variants[variant], sizes[size], className)} {...rest} />;
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  active?: boolean;
  children: ReactNode;
}

/** Square, icon-only button (rail toggles, tab close, toolbar actions). */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, active = false, className, type = 'button', children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      aria-pressed={active}
      className={cn(
        'inline-flex h-7 w-7 items-center justify-center rounded-md transition duration-fast ease-out',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50',
        active ? 'bg-fill/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
        className,
      )}
      {...rest}
    >
      {children}
    </button>
  );
});
