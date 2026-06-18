/* Tailwind preset that maps the CSS design tokens into the theme, so SDK and service
   components use semantic utilities (bg-surface, text-primary, rounded-card, shadow-elev-2,
   backdrop-blur-vibrancy, bg-material-thick, animate-pop-in, squircle). Consumed by the
   app's tailwind.config.ts. */

const v = (name: string) => `var(--${name})`;
// Channel-based tokens: enables Tailwind /opacity modifiers (bg-accent/15, ring-accent/50, …).
const rgb = (name: string) => `rgb(var(--${name}) / <alpha-value>)`;

// Superellipse ("squircle") mask — continuous-curvature corners like an iOS app icon.
// Stretched to the element via mask-size:100% 100%. Apply only to solid-fill surfaces
// (the brand tile): masking clips shadows/focus rings, so it's wrong for buttons/cards.
const SQUIRCLE_MASK =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100' preserveAspectRatio='none'%3E%3Cpath d='M0,50 C0,7 7,0 50,0 C93,0 100,7 100,50 C100,93 93,100 50,100 C7,100 0,93 0,50 Z' fill='%23000'/%3E%3C/svg%3E\")";

const preset = {
  darkMode: ['selector', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        accent: { DEFAULT: rgb('accent'), hover: v('accent-hover'), fg: v('accent-fg') },
        bg: { base: v('bg-base') },
        surface: { DEFAULT: v('surface'), raised: v('surface-raised'), sidebar: v('sidebar') },
        material: {
          ultrathin: v('material-ultrathin'),
          thin: v('material-thin'),
          regular: v('material-regular'),
          thick: v('material-thick'),
        },
        separator: v('separator'),
        // Neutral fill for hover/tint backgrounds (use with /opacity, e.g. bg-fill/10).
        fill: rgb('fill'),
        text: { primary: v('text-primary'), secondary: v('text-secondary'), tertiary: v('text-tertiary') },
        success: rgb('success'),
        warning: rgb('warning'),
        danger: rgb('danger'),
        // Per-component identity colors (hostek metrics): bg-cpu/15, text-gpu, stroke-ssd, …
        cpu: rgb('cpu'),
        ram: rgb('ram'),
        gpu: rgb('gpu'),
        net: rgb('net'),
        ssd: rgb('ssd'),
      },
      borderRadius: {
        xs: v('radius-xs'),
        sm: v('radius-sm'),
        md: v('radius-md'),
        card: v('radius-card'),
        lg: v('radius-lg'),
        '2xl': v('radius-2xl'),
      },
      boxShadow: {
        'elev-1': v('shadow-1'),
        'elev-2': v('shadow-2'),
        'elev-3': v('shadow-3'),
      },
      backdropBlur: { vibrancy: '30px', thin: '20px' },
      fontFamily: {
        sans: ['-apple-system', 'SF Pro Text', 'SF Pro Display', 'Inter', 'system-ui', 'sans-serif'],
        mono: ['SF Mono', 'JetBrains Mono', 'ui-monospace', 'Menlo', 'monospace'],
      },
      fontSize: {
        caption: ['12px', { lineHeight: '1.3', letterSpacing: '-0.01em' }],
        footnote: ['13px', { lineHeight: '1.35', letterSpacing: '-0.01em' }],
        subhead: ['15px', { lineHeight: '1.4', letterSpacing: '-0.015em' }],
        body: ['17px', { lineHeight: '1.45', letterSpacing: '-0.022em' }],
        title3: ['20px', { lineHeight: '1.3', letterSpacing: '-0.015em' }],
        title2: ['22px', { lineHeight: '1.25', letterSpacing: '-0.018em' }],
        title1: ['28px', { lineHeight: '1.18', letterSpacing: '-0.02em' }],
        largeTitle: ['34px', { lineHeight: '1.15', letterSpacing: '-0.022em' }],
      },
      transitionTimingFunction: { out: v('ease-out'), spring: v('ease-spring') },
      transitionDuration: { fast: '120ms', base: '220ms', slow: '360ms' },
      keyframes: {
        'overlay-in': { from: { opacity: '0' }, to: { opacity: '1' } },
        'overlay-out': { from: { opacity: '1' }, to: { opacity: '0' } },
        'pop-in': {
          from: { opacity: '0', transform: 'scale(0.96)' },
          to: { opacity: '1', transform: 'scale(1)' },
        },
        'pop-out': {
          from: { opacity: '1', transform: 'scale(1)' },
          to: { opacity: '0', transform: 'scale(0.96)' },
        },
        'sheet-in-right': { from: { transform: 'translateX(100%)' }, to: { transform: 'translateX(0)' } },
        'sheet-out-right': { from: { transform: 'translateX(0)' }, to: { transform: 'translateX(100%)' } },
        'sheet-in-left': { from: { transform: 'translateX(-100%)' }, to: { transform: 'translateX(0)' } },
        'sheet-out-left': { from: { transform: 'translateX(0)' }, to: { transform: 'translateX(-100%)' } },
        'toast-in': {
          from: { opacity: '0', transform: 'translateY(8px) scale(0.98)' },
          to: { opacity: '1', transform: 'translateY(0) scale(1)' },
        },
        'toast-out': {
          from: { opacity: '1', transform: 'translateY(0) scale(1)' },
          to: { opacity: '0', transform: 'translateY(6px) scale(0.98)' },
        },
      },
      animation: {
        'overlay-in': 'overlay-in 220ms cubic-bezier(0.22,1,0.36,1)',
        'overlay-out': 'overlay-out 160ms cubic-bezier(0.22,1,0.36,1)',
        'pop-in': 'pop-in 220ms cubic-bezier(0.34,1.56,0.64,1)',
        'pop-out': 'pop-out 140ms cubic-bezier(0.22,1,0.36,1)',
        'sheet-in-right': 'sheet-in-right 320ms cubic-bezier(0.22,1,0.36,1)',
        'sheet-out-right': 'sheet-out-right 240ms cubic-bezier(0.22,1,0.36,1)',
        'sheet-in-left': 'sheet-in-left 320ms cubic-bezier(0.22,1,0.36,1)',
        'sheet-out-left': 'sheet-out-left 240ms cubic-bezier(0.22,1,0.36,1)',
        'toast-in': 'toast-in 280ms cubic-bezier(0.34,1.56,0.64,1)',
        'toast-out': 'toast-out 200ms cubic-bezier(0.22,1,0.36,1) forwards',
      },
    },
  },
  plugins: [
    function squirclePlugin({ addUtilities }: { addUtilities: (u: Record<string, Record<string, string>>) => void }) {
      addUtilities({
        '.squircle': {
          '-webkit-mask-image': SQUIRCLE_MASK,
          'mask-image': SQUIRCLE_MASK,
          '-webkit-mask-size': '100% 100%',
          'mask-size': '100% 100%',
          '-webkit-mask-repeat': 'no-repeat',
          'mask-repeat': 'no-repeat',
        },
      });
    },
  ],
};

export default preset;
