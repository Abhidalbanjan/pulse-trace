"use client";

import React from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useAuth } from '@/context/AuthContext';
import { useTheme } from '@/context/ThemeContext';
import { NAV_ICONS } from '@/lib/theme';

export function GlobalSidebar({ isCollapsed, onToggle }: { isCollapsed?: boolean, onToggle?: () => void }) {
  const pathname = usePathname();
  const { user, logout } = useAuth();
  const { tokens: t } = useTheme();

  const navItems = [
    { key: 'home', label: 'AI SRE', path: '/' },
    { key: 'incidents', label: 'Incidents', path: '/incidents' },
    { key: 'deployments', label: 'Deploy Gates', path: '/deployments' },
    { key: 'onboarding', label: 'Onboarding', path: '/onboarding' },
    { key: 'explorer', label: 'Log Explorer', path: '/explorer' },
    { key: 'traces', label: 'Distributed Traces', path: '/traces' },
    { key: 'services', label: 'Services', path: '/services' },
    { key: 'metrics', label: 'Metrics', path: '/metrics' },
    { key: 'errors', label: 'Error Tracking', path: '/errors' },
    { key: 'profiler', label: 'Continuous Profiler', path: '/profiler' },
    { key: 'rum', label: 'Real User Monitoring', path: '/rum' },
    { key: 'synthetics', label: 'Synthetic Monitoring', path: '/synthetics' },
    { key: 'topology', label: 'Topology', path: '/topology' },
    { key: 'catalog', label: 'Catalog', path: '/catalog' },
    { key: 'settings', label: 'Settings', path: '/settings' },
  ];

  return (
    <aside style={{
      width: isCollapsed ? '84px' : '250px',
      height: '100vh',
      position: 'sticky',
      top: 0,
      flexShrink: 0,
      padding: '18px 12px',
      display: 'flex',
      flexDirection: 'column',
      background: t.panelBg,
      borderRight: `1px solid ${t.panelBorder}`,
      borderTop: `1px solid ${t.panelTop}`,
      backdropFilter: 'blur(34px) saturate(180%)',
      WebkitBackdropFilter: 'blur(34px) saturate(180%)',
      boxShadow: t.shadow,
      transition: 'width 0.25s ease',
      zIndex: 100,
      overflowX: 'hidden',
    }}>
      {/* Brand & Toggle */}
      <div
        onClick={onToggle}
        style={{
          display: 'flex', alignItems: 'center', gap: '10px',
          padding: isCollapsed ? '4px 2px 22px' : '4px 10px 22px',
          cursor: 'pointer',
          justifyContent: isCollapsed ? 'center' : 'flex-start',
        }}
      >
        <div style={{
          width: '30px', height: '30px', borderRadius: '9px',
          background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
          display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
          overflow: 'hidden',
        }}>
          <img src="/logo.png" alt="" style={{ width: '20px', height: '20px', objectFit: 'contain' }} />
        </div>
        {!isCollapsed && (
          <span style={{ fontSize: '17px', fontWeight: 700, letterSpacing: '-0.01em', color: t.text1, whiteSpace: 'nowrap' }}>
            PulseTrace
          </span>
        )}
      </div>

      {/* Navigation */}
      <nav style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '3px', overflowY: 'auto' }}>
        {navItems.map((item) => {
          const isActive = pathname === item.path;
          return (
            <Link key={item.key} href={item.path} style={{ textDecoration: 'none' }} title={isCollapsed ? item.label : undefined}>
              <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: isCollapsed ? 'center' : 'flex-start',
                gap: '12px',
                padding: isCollapsed ? '10px 0' : '10px 14px',
                borderRadius: '12px',
                background: isActive ? t.accentSoft : 'transparent',
                color: isActive ? t.text1 : t.text2,
                fontSize: '14px',
                fontWeight: isActive ? 600 : 500,
                marginBottom: '1px',
                transition: 'background 0.15s ease',
              }}
              onMouseEnter={(e) => {
                if (!isActive) e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(255,255,255,0.4)';
              }}
              onMouseLeave={(e) => {
                if (!isActive) e.currentTarget.style.background = 'transparent';
              }}>
                <span className="material-symbols-outlined" style={{ fontSize: '19px', color: isActive ? t.accent : 'inherit', flexShrink: 0 }}>
                  {NAV_ICONS[item.key]}
                </span>
                {!isCollapsed && (
                  <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{item.label}</span>
                )}
              </div>
            </Link>
          );
        })}
      </nav>

      {/* Profile row */}
      <div style={{
        marginTop: 'auto', display: 'flex', alignItems: 'center', gap: '10px',
        padding: isCollapsed ? '10px 0' : '10px',
        justifyContent: isCollapsed ? 'center' : 'flex-start',
      }}>
        <div style={{
          width: '30px', height: '30px', borderRadius: '50%',
          background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
          color: '#fff', display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: '13px', fontWeight: 700, flexShrink: 0,
        }} title={user?.email}>
          {user?.email?.[0]?.toUpperCase() || 'U'}
        </div>
        {!isCollapsed && (
          <>
            <div style={{ flex: 1, overflow: 'hidden' }}>
              <div style={{ fontSize: '13px', fontWeight: 600, color: t.text1, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {user?.email || 'System Admin'}
              </div>
              <div style={{ fontSize: '11px', color: t.accent }}>{user?.role || 'admin'}</div>
            </div>
            <button
              onClick={() => {
                if (window.confirm('Are you sure you want to log out?')) {
                  logout();
                }
              }}
              style={{
                background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer',
                padding: '4px', display: 'flex', alignItems: 'center', justifyContent: 'center',
                transition: 'color 0.2s', flexShrink: 0,
              }}
              onMouseEnter={(e) => (e.currentTarget.style.color = t.red)}
              onMouseLeave={(e) => (e.currentTarget.style.color = t.text2)}
              title="Log Out"
            >
              <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>logout</span>
            </button>
          </>
        )}
      </div>
    </aside>
  );
}
