export type Theme = 'light' | 'dark';

export interface ThemeTokens {
  dark: boolean;
  pageBg: string;
  panelBg: string;
  panelBorder: string;
  panelTop: string;
  text1: string;
  text2: string;
  text3: string;
  accent: string;
  accent2: string;
  accentSoft: string;
  green: string;
  amber: string;
  red: string;
  redSoft: string;
  gold: string;
  shadow: string;
  pillBg: string;
}

export function getTokens(theme: Theme): ThemeTokens {
  const dark = theme === 'dark';
  return {
    dark,
    pageBg: dark
      ? 'radial-gradient(circle at 12% 18%, rgba(70,90,160,0.22), transparent 40%), radial-gradient(circle at 88% 12%, rgba(140,60,120,0.16), transparent 38%), radial-gradient(circle at 60% 90%, rgba(40,110,110,0.16), transparent 42%), #0b0c10'
      : 'radial-gradient(circle at 12% 18%, rgba(120,140,255,0.20), transparent 40%), radial-gradient(circle at 88% 12%, rgba(255,150,200,0.14), transparent 38%), radial-gradient(circle at 60% 90%, rgba(120,220,210,0.16), transparent 42%), #eef0f5',
    panelBg: dark ? 'rgba(36,38,48,0.5)' : 'rgba(255,255,255,0.55)',
    panelBorder: dark ? 'rgba(255,255,255,0.09)' : 'rgba(255,255,255,0.75)',
    panelTop: dark ? 'rgba(255,255,255,0.14)' : 'rgba(255,255,255,0.95)',
    text1: dark ? 'rgba(240,241,245,0.94)' : 'rgba(28,30,38,0.92)',
    text2: dark ? 'rgba(240,241,245,0.56)' : 'rgba(28,30,38,0.55)',
    text3: dark ? 'rgba(240,241,245,0.36)' : 'rgba(28,30,38,0.38)',
    accent: '#5B6CFF',
    accent2: '#3FC7D6',
    accentSoft: dark ? 'rgba(91,108,255,0.16)' : 'rgba(91,108,255,0.1)',
    green: dark ? '#34C77E' : '#25A96B',
    amber: dark ? '#E6A93A' : '#D98F1E',
    red: dark ? '#F16B63' : '#E0524B',
    redSoft: dark ? 'rgba(241,107,99,0.14)' : 'rgba(224,82,75,0.08)',
    gold: '#E8B84B',
    shadow: dark ? '0 24px 48px -24px rgba(0,0,0,0.55)' : '0 24px 48px -24px rgba(60,70,140,0.18)',
    pillBg: dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.6)',
  };
}

// Sidebar.dc.html navDefs — key -> Material Symbols Outlined icon name
export const NAV_ICONS: Record<string, string> = {
  home: 'auto_awesome',
  incidents: 'warning',
  alerts: 'notifications_active',
  slo: 'target',
  deployments: 'shield',
  onboarding: 'bolt',
  explorer: 'manage_search',
  query: 'terminal',
  traces: 'timeline',
  services: 'monitor_heart',
  metrics: 'show_chart',
  errors: 'bug_report',
  profiler: 'speed',
  rum: 'groups',
  synthetics: 'public',
  topology: 'hub',
  catalog: 'menu_book',
  settings: 'settings',
};
