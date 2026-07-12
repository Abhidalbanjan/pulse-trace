"use client";

import React from 'react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useAuth } from '@/context/AuthContext';

export function GlobalSidebar({ isCollapsed, onToggle }: { isCollapsed?: boolean, onToggle?: () => void }) {
  const pathname = usePathname();
  const { user, logout } = useAuth();

  const navItems = [
    { icon: '✨', label: 'AI SRE', path: '/' },
    { icon: '🚨', label: 'Incidents', path: '/incidents' },
    { icon: '🛡️', label: 'Deploy Gates', path: '/deployments' },
    { icon: '⚡', label: 'Onboarding', path: '/onboarding' },
    { icon: '◫', label: 'Log Explorer', path: '/explorer' },
    { icon: '🌊', label: 'Distributed Traces', path: '/traces' },
    { icon: '🔥', label: 'Continuous Profiler', path: '/profiler' },
    { icon: '👥', label: 'Real User Monitoring', path: '/rum' },
    { icon: '🌐', label: 'Synthetic Monitoring', path: '/synthetics' },
    { icon: '⎉', label: 'Topology', path: '/topology' },
    { icon: '📚', label: 'Catalog', path: '/catalog' },
    { icon: '⚙', label: 'Settings', path: '/settings' },
  ];

  return (
    <aside style={{
      width: isCollapsed ? '80px' : '260px',
      height: '100vh',
      position: 'fixed',
      left: 0,
      top: 0,
      display: 'flex',
      flexDirection: 'column',
      padding: '24px 0',
      zIndex: 100,
      borderRight: '1px solid var(--border-color)',
      transition: 'width 0.3s ease',
      overflowX: 'hidden'
    }}>
      {/* Brand & Toggle */}
      <div style={{ padding: '0 24px', marginBottom: '40px', display: 'flex', alignItems: 'center', justifyContent: isCollapsed ? 'center' : 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px', cursor: 'pointer' }} onClick={onToggle}>
          <div style={{ 
            width: '32px', height: '32px', 
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            flexShrink: 0
          }}>
            <img src="/logo.png" alt="PulseTrace Logo" style={{ width: '100%', height: '100%', objectFit: 'contain' }} />
          </div>
          {!isCollapsed && (
            <span style={{ fontSize: '20px', fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--text-primary)', whiteSpace: 'nowrap' }}>
              Pulse<span style={{ color: 'var(--accent-blue)' }}>Trace</span>
            </span>
          )}
        </div>
        {!isCollapsed && (
          <button onClick={onToggle} style={{ background: 'transparent', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer' }}>
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><polyline points="15 18 9 12 15 6"></polyline></svg>
          </button>
        )}
      </div>

      {/* Navigation */}
      <nav style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '4px', padding: '0 12px' }}>
        {navItems.map((item) => {
          const isActive = pathname === item.path;
          return (
            <Link key={item.label} href={item.path} style={{ textDecoration: 'none' }} title={isCollapsed ? item.label : undefined}>
              <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: isCollapsed ? 'center' : 'flex-start',
                gap: '12px',
                padding: isCollapsed ? '12px 0' : '12px',
                borderRadius: '8px',
                color: isActive ? 'var(--text-primary)' : 'var(--text-secondary)',
                background: isActive ? 'rgba(255,255,255,0.05)' : 'transparent',
                transition: 'all 0.2s ease',
                cursor: 'pointer',
                borderLeft: isActive && !isCollapsed ? '3px solid var(--accent-blue)' : (isCollapsed ? 'none' : '3px solid transparent')
              }}
              onMouseEnter={(e) => {
                if (!isActive) e.currentTarget.style.background = 'rgba(255,255,255,0.02)';
              }}
              onMouseLeave={(e) => {
                if (!isActive) e.currentTarget.style.background = 'transparent';
              }}>
                <span style={{ fontSize: '18px', width: '24px', textAlign: 'center', color: isActive ? 'var(--accent-blue)' : 'inherit', flexShrink: 0 }}>
                  {item.icon}
                </span>
                {!isCollapsed && (
                  <span style={{ fontSize: '14px', fontWeight: 500, whiteSpace: 'nowrap' }}>{item.label}</span>
                )}
              </div>
            </Link>
          );
        })}
      </nav>

      {/* User Profile */}
      <div style={{ padding: '0 16px', marginTop: 'auto' }}>
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '12px',
          padding: isCollapsed ? '8px' : '12px',
          background: 'rgba(0,0,0,0.2)',
          borderRadius: '8px',
          border: '1px solid var(--border-color)',
          justifyContent: isCollapsed ? 'center' : 'flex-start'
        }}>
          <div style={{
            width: '32px', height: '32px', borderRadius: '50%',
            background: 'linear-gradient(135deg, var(--accent-blue), var(--accent-purple))',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: '14px', fontWeight: 600, flexShrink: 0
          }} title={user?.email}>
            {user?.email?.[0]?.toUpperCase() || 'U'}
          </div>
          {!isCollapsed && (
            <>
              <div style={{ flex: 1, overflow: 'hidden' }}>
                <div style={{ fontSize: '13px', fontWeight: 600, color: 'var(--text-primary)', whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' }}>
                  {user?.email || 'System Admin'}
                </div>
                <div style={{ fontSize: '11px', color: 'var(--accent-blue)' }}>{user?.role || 'admin'}</div>
              </div>
              <button  
            onClick={() => {
              if (window.confirm("Are you sure you want to log out?")) {
                logout();
              }
            }}
            style={{ 
              background: 'transparent', 
              border: 'none', 
              color: 'var(--text-secondary)', 
              cursor: 'pointer', 
              padding: '4px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              transition: 'color 0.2s'
            }}
            onMouseEnter={(e) => e.currentTarget.style.color = 'var(--status-red)'}
            onMouseLeave={(e) => e.currentTarget.style.color = 'var(--text-secondary)'}
            title="Log Out"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path>
              <polyline points="16 17 21 12 16 7"></polyline>
              <line x1="21" y1="12" x2="9" y2="12"></line>
            </svg>
          </button>
          </>
          )}
        </div>
      </div>
    </aside>
  );
}
