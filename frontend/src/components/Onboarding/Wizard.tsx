"use client";

import React, { useState } from 'react';
import { CodeSnippet } from './CodeSnippet';

type Platform = 'Kubernetes' | 'Docker' | 'Node.js' | 'Go' | null;

export function Wizard() {
  const [step, setStep] = useState(1);
  const [platform, setPlatform] = useState<Platform>(null);
  const [apiKey, setApiKey] = useState<string | null>(null);

  const platforms = [
    { name: 'Kubernetes', desc: 'Helm chart & DaemonSet', icon: '⎈' },
    { name: 'Docker', desc: 'Container logs & metrics', icon: '🐳' },
    { name: 'Node.js', desc: 'OTel JS Auto-instrumentation', icon: '⬢' },
    { name: 'Go', desc: 'Native Go agent & Profiling', icon: '🐹' },
  ];

  const generateKey = () => {
    // Simulate API call
    setTimeout(() => {
      setApiKey(`pt_${Math.random().toString(36).substring(2, 15)}_${Math.random().toString(36).substring(2, 15)}`);
      setStep(3);
    }, 600);
  };

  const getScriptForPlatform = () => {
    if (!apiKey) return '';
    switch (platform) {
      case 'Kubernetes':
        return `helm repo add pulsetrace https://charts.pulsetrace.com\nhelm install pt-agent pulsetrace/agent \\\n  --set apiKey=${apiKey} \\\n  --namespace pulsetrace-system --create-namespace`;
      case 'Docker':
        return `docker run -d --name pulsetrace-agent \\\n  -e PT_API_KEY=${apiKey} \\\n  -v /var/run/docker.sock:/var/run/docker.sock \\\n  pulsetrace/agent:latest`;
      case 'Node.js':
        return `npm install @pulsetrace/agent\n\n// Add to top of your entry file (index.js):\nrequire('@pulsetrace/agent').init({ apiKey: '${apiKey}' });`;
      case 'Go':
        return `go get github.com/pulsetrace/agent-go\n\n// Add to main.go:\nimport "github.com/pulsetrace/agent-go"\n\nfunc main() {\n    agent.Init("${apiKey}")\n    defer agent.Flush()\n}`;
      default:
        return '';
    }
  };

  return (
    <div className="glass-panel" style={{ padding: '40px', maxWidth: '800px', margin: '0 auto' }}>
      
      {/* Progress Indicator */}
      <div style={{ display: 'flex', gap: '12px', marginBottom: '40px' }}>
        {[1, 2, 3].map((s) => (
          <div key={s} style={{ 
            flex: 1, 
            height: '4px', 
            background: step >= s ? 'var(--status-green)' : 'rgba(255,255,255,0.1)',
            borderRadius: '2px',
            transition: 'background 0.3s ease'
          }} />
        ))}
      </div>

      {step === 1 && (
        <div style={{ animation: 'fadeIn 0.5s ease' }}>
          <h2 style={{ fontSize: '28px', marginBottom: '8px' }}>Welcome to PulseTrace.</h2>
          <p style={{ color: 'var(--text-secondary)', marginBottom: '32px' }}>Let's get your telemetry flowing. Where is your application running?</p>
          
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '16px' }}>
            {platforms.map(p => (
              <div 
                key={p.name}
                onClick={() => setPlatform(p.name as Platform)}
                style={{
                  padding: '24px',
                  borderRadius: '12px',
                  border: platform === p.name ? '1px solid var(--status-green)' : '1px solid var(--border-color)',
                  background: platform === p.name ? 'rgba(0, 255, 128, 0.05)' : 'rgba(255,255,255,0.02)',
                  cursor: 'pointer',
                  transition: 'all 0.2s ease'
                }}
              >
                <div style={{ fontSize: '32px', marginBottom: '12px' }}>{p.icon}</div>
                <h3 style={{ fontSize: '18px', margin: '0 0 4px 0' }}>{p.name}</h3>
                <p style={{ margin: 0, fontSize: '14px', color: 'var(--text-secondary)' }}>{p.desc}</p>
              </div>
            ))}
          </div>

          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '32px' }}>
            <button 
              disabled={!platform}
              onClick={() => setStep(2)}
              style={{
                background: platform ? 'white' : 'rgba(255,255,255,0.1)',
                color: platform ? 'black' : 'rgba(255,255,255,0.3)',
                padding: '12px 24px',
                borderRadius: '8px',
                border: 'none',
                fontWeight: 600,
                cursor: platform ? 'pointer' : 'not-allowed',
                transition: 'all 0.2s'
              }}
            >
              Continue
            </button>
          </div>
        </div>
      )}

      {step === 2 && (
        <div style={{ animation: 'fadeIn 0.5s ease' }}>
          <h2 style={{ fontSize: '28px', marginBottom: '8px' }}>Generate Ingestion Key</h2>
          <p style={{ color: 'var(--text-secondary)', marginBottom: '32px' }}>
            This key will allow your {platform} environment to securely send traces, logs, and metrics to PulseTrace.
          </p>

          <div style={{ 
            background: 'rgba(0,0,0,0.4)', 
            padding: '32px', 
            borderRadius: '12px', 
            textAlign: 'center',
            border: '1px solid var(--border-color)'
          }}>
            <button 
              onClick={generateKey}
              style={{
                background: 'linear-gradient(135deg, #00D2FF 0%, #3A7BD5 100%)',
                color: 'white',
                padding: '14px 28px',
                borderRadius: '8px',
                border: 'none',
                fontWeight: 600,
                fontSize: '16px',
                cursor: 'pointer',
                boxShadow: '0 4px 14px rgba(0, 210, 255, 0.3)'
              }}
            >
              Generate API Key
            </button>
          </div>
        </div>
      )}

      {step === 3 && (
        <div style={{ animation: 'fadeIn 0.5s ease' }}>
          <h2 style={{ fontSize: '28px', marginBottom: '8px' }}>You're all set!</h2>
          <p style={{ color: 'var(--text-secondary)', marginBottom: '32px' }}>
            Run this command in your {platform} environment to start streaming telemetry.
          </p>
          
          <CodeSnippet code={getScriptForPlatform()} language={platform === 'Node.js' || platform === 'Go' ? 'javascript' : 'bash'} />
          
          <div style={{ background: 'rgba(0,255,128,0.1)', color: 'var(--status-green)', padding: '16px', borderRadius: '8px', marginTop: '24px', display: 'flex', alignItems: 'center', gap: '12px', fontSize: '14px' }}>
            <span>⚡️</span>
            <div>
              <strong>Waiting for data...</strong>
              <div style={{ opacity: 0.8 }}>Once you run the command, data will appear on your dashboard within 3 seconds.</div>
            </div>
          </div>

          <div style={{ display: 'flex', justifyContent: 'center', marginTop: '40px' }}>
            <a href="/" style={{ color: 'white', textDecoration: 'none', background: 'rgba(255,255,255,0.1)', padding: '10px 20px', borderRadius: '6px', fontSize: '14px' }}>
              Return to Dashboard
            </a>
          </div>
        </div>
      )}

    </div>
  );
}
