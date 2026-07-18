"use client";

import React, { createContext, useContext, useEffect, useState } from 'react';
import { Theme, ThemeTokens, getTokens } from '@/lib/theme';

interface ThemeContextType {
  theme: Theme;
  tokens: ThemeTokens;
  toggleTheme: () => void;
}

const ThemeContext = createContext<ThemeContextType>({
  theme: 'light',
  tokens: getTokens('light'),
  toggleTheme: () => {},
});

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setTheme] = useState<Theme>('light');

  useEffect(() => {
    const saved = localStorage.getItem('pt-theme');
    if (saved === 'light' || saved === 'dark') setTheme(saved);
  }, []);

  const toggleTheme = () => {
    setTheme(prev => {
      const next = prev === 'dark' ? 'light' : 'dark';
      localStorage.setItem('pt-theme', next);
      return next;
    });
  };

  return (
    <ThemeContext.Provider value={{ theme, tokens: getTokens(theme), toggleTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}

export const useTheme = () => useContext(ThemeContext);
