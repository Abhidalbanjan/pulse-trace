"use client";

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';

interface CodeSnippetProps {
  code: string;
  language?: string;
}

export function CodeSnippet({ code, language = 'bash' }: CodeSnippetProps) {
  const { tokens: t } = useTheme();
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard.writeText(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div style={{
      background: t.dark ? 'rgba(0,0,0,0.35)' : 'rgba(0,0,0,0.05)',
      border: '1px solid ' + t.panelBorder,
      borderRadius: '12px',
      overflow: 'hidden',
      marginTop: '16px'
    }}>
      <div style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        padding: '10px 16px',
        background: t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)',
        borderBottom: '1px solid ' + t.panelBorder,
        fontSize: '12px',
        color: t.text2
      }}>
        <span>{language}</span>
        <button
          onClick={handleCopy}
          style={{
            background: 'transparent',
            border: 'none',
            color: copied ? t.accent : t.text1,
            cursor: 'pointer',
            fontSize: '12px',
            fontWeight: 600,
            display: 'flex',
            alignItems: 'center',
            gap: '4px'
          }}
        >
          <span className="material-symbols-outlined" style={{ fontSize: '14px' }}>
            {copied ? 'check' : 'content_copy'}
          </span>
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre style={{
        margin: 0,
        padding: '16px',
        overflowX: 'auto',
        fontSize: '13px',
        fontFamily: 'monospace',
        lineHeight: '1.6',
        color: t.text1
      }}>
        <code>{code}</code>
      </pre>
    </div>
  );
}
