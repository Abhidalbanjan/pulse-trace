"use client";

import React from 'react';
import { useAuth } from '@/context/AuthContext';
import { useTheme } from '@/context/ThemeContext';

export function GlobalHeader() {
  const { tokens: t, theme, toggleTheme } = useTheme();
  const { user } = useAuth();

  const pillStyle: React.CSSProperties = {
    display: 'flex', alignItems: 'center', gap: '6px', background: t.pillBg,
    border: `1px solid ${t.panelBorder}`, borderRadius: '100px', padding: '8px 16px',
    fontSize: '13px', color: t.text2, whiteSpace: 'nowrap',
  };
  const selectStyle: React.CSSProperties = {
    background: 'transparent', border: 'none', color: t.text2, outline: 'none',
    fontSize: '13px', fontFamily: 'inherit', cursor: 'pointer',
  };
  const iconBtnStyle: React.CSSProperties = {
    width: '36px', height: '36px', borderRadius: '50%', border: 'none', background: 'transparent',
    color: t.text2, cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
  };

  return (
    <header style={{
      height: '76px', display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      padding: '0 28px', margin: '18px 20px 0', borderRadius: '20px',
      background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderTop: `1px solid ${t.panelTop}`,
      backdropFilter: 'blur(34px) saturate(180%)', WebkitBackdropFilter: 'blur(34px) saturate(180%)',
      boxShadow: t.shadow, position: 'sticky', top: '18px', zIndex: 90,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        <div style={pillStyle}>
          <span className="material-symbols-outlined" style={{ fontSize: '15px' }}>dns</span>
          <select style={selectStyle} defaultValue="prod" aria-label="Global environment">
            <option value="all">All Environments</option>
            <option value="prod">Production</option>
            <option value="staging">Staging</option>
          </select>
        </div>
        <div style={pillStyle}>
          <span className="material-symbols-outlined" style={{ fontSize: '15px' }}>schedule</span>
          <select style={selectStyle} defaultValue="1h" aria-label="Global time window">
            <option value="15m">Last 15 minutes</option>
            <option value="1h">Last 1 hour</option>
            <option value="24h">Last 24 hours</option>
          </select>
        </div>
      </div>

      <div style={{
        flex: 1, maxWidth: '380px', margin: '0 24px', display: 'flex', alignItems: 'center', gap: '8px',
        background: t.pillBg, border: `1px solid ${t.panelBorder}`, borderRadius: '100px', padding: '9px 16px',
      }}>
        <span className="material-symbols-outlined" style={{ fontSize: '17px', color: t.text3 }}>search</span>
        <input
          type="text"
          placeholder="Search traces, logs, services…"
          style={{ flex: 1, background: 'transparent', border: 'none', outline: 'none', fontFamily: 'inherit', fontSize: '13px', color: t.text1 }}
        />
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
        <button style={iconBtnStyle} onClick={toggleTheme} title="Toggle theme">
          <span className="material-symbols-outlined" style={{ fontSize: '20px' }}>{theme === 'dark' ? 'light_mode' : 'dark_mode'}</span>
        </button>
        <button style={iconBtnStyle} title="Notifications">
          <span className="material-symbols-outlined" style={{ fontSize: '20px' }}>notifications</span>
        </button>
        <div style={{
          width: '32px', height: '32px', borderRadius: '50%',
          background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
          display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: '13px', fontWeight: 700,
        }} title={user?.email}>
          {user?.email?.[0]?.toUpperCase() || 'U'}
        </div>
      </div>
    </header>
  );
}
