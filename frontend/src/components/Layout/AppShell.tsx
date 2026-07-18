"use client";

import React, { useState, useEffect } from 'react';
import { usePathname } from 'next/navigation';
import { GlobalSidebar } from './GlobalSidebar';
import { GlobalHeader } from './GlobalHeader';
import { initRUM } from '@/lib/rum';
import { useTheme } from '@/context/ThemeContext';

export function AppShell({ children }: { children: React.ReactNode }) {
  const [isSidebarCollapsed, setIsSidebarCollapsed] = useState(false);
  const pathname = usePathname();
  const isAuthPage = pathname === '/login' || pathname === '/register';
  const { tokens: t } = useTheme();

  useEffect(() => {
    initRUM();
  }, []);

  if (isAuthPage) {
    return <>{children}</>;
  }

  return (
    <div style={{ display: 'flex', minHeight: '100vh', width: '100%', background: t.pageBg, color: t.text1 }}>
      <GlobalSidebar isCollapsed={isSidebarCollapsed} onToggle={() => setIsSidebarCollapsed(!isSidebarCollapsed)} />
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        <GlobalHeader />
        <main style={{ flex: 1, padding: '20px 28px 28px', overflowY: 'auto' }}>
          {children}
        </main>
      </div>
    </div>
  );
}
