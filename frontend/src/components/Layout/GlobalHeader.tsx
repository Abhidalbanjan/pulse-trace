"use client";

import React from 'react';

export function GlobalHeader() {
  return (
    <header style={{
      height: '64px',
      borderBottom: '1px solid var(--border-color)',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      padding: '0 24px',
      background: 'var(--apple-glass-bg)',
      backdropFilter: 'var(--apple-glass-blur)',
      WebkitBackdropFilter: 'var(--apple-glass-blur)',
      borderBottom: '1px solid var(--apple-glass-border)',
      boxShadow: '0 4px 24px rgba(0,0,0,0.2)',
      position: 'sticky',
      top: 0,
      zIndex: 90
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '24px' }}>
        <h1 style={{ fontSize: '18px', fontWeight: 600, margin: 0, letterSpacing: '1px' }}>
          <span style={{ color: '#FFFFFF' }}>PULSE</span>
          <span className="text-gradient">TRACE</span>
        </h1>
        
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', background: 'rgba(255,255,255,0.05)', padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--border-color)' }}>
          <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>Env:</span>
          <select style={{ background: 'transparent', border: 'none', color: 'var(--text-primary)', outline: 'none', fontSize: '14px', fontFamily: 'inherit' }}>
            <option value="all">All Environments</option>
            <option value="prod">Production</option>
            <option value="staging">Staging</option>
          </select>
        </div>
      </div>

      <div style={{ flex: 1, maxWidth: '400px', margin: '0 24px' }}>
        <div style={{ 
          display: 'flex', 
          alignItems: 'center', 
          background: 'rgba(0,0,0,0.2)', 
          border: '1px solid var(--border-color)',
          borderRadius: '8px',
          padding: '8px 12px'
        }}>
          <span style={{ color: 'var(--text-secondary)', marginRight: '8px' }}>🔍</span>
          <input 
            type="text" 
            placeholder="Search traces, logs, services..." 
            style={{ 
              background: 'transparent', 
              border: 'none', 
              color: 'var(--text-primary)', 
              width: '100%',
              outline: 'none',
              fontFamily: 'inherit'
            }} 
          />
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: '16px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>Time:</span>
          <select style={{ background: 'transparent', border: 'none', color: 'var(--text-primary)', outline: 'none', fontSize: '14px', fontFamily: 'inherit' }}>
            <option>Last 15 minutes</option>
            <option>Last 1 hour</option>
            <option>Last 24 hours</option>
          </select>
        </div>
        <button className="icon-btn">🔔</button>
      </div>
    </header>
  );
}
