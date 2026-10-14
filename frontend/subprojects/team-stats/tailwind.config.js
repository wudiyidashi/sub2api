/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ['class', '[data-theme="dark"]'],
  content: ['./index.html', './src/**/*.{vue,ts,tsx}'],
  theme: {
    extend: {
      colors: {
        bg: 'var(--bg)',
        surface: 'var(--surface)',
        border: 'var(--border)',
        text: 'var(--text)',
        muted: 'var(--muted)',
        accent: 'var(--accent)',
        success: 'var(--success)',
        error: 'var(--error)',
        warning: 'var(--warning)',
        'surface-hover': 'var(--surface-hover)',
        'accent-dim': 'var(--accent-dim)',
        'google-blue': 'var(--google-blue)',
        'google-red': 'var(--google-red)',
        'google-yellow': 'var(--google-yellow)',
        'google-green': 'var(--google-green)',
      },
    },
  },
  plugins: [],
};
